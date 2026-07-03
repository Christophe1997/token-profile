package agentsview

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeMultiAgentBinary writes an executable shell-script fixture standing in
// for the real agentsview binary across both `session list` and `usage
// daily`, dispatching to per-fixture files placed alongside it. Mirrors
// fakeAgentsview's single-script pattern (client_test.go), extended for the
// multi-subcommand scenarios Resolve needs.
//
// sessionPages maps a cursor value (""  for the first page) to that page's
// `session list --json` body. usageByAgent maps an agent name ("" for the
// unfiltered/no --agent call) to that call's `usage daily --json` body.
func fakeMultiAgentBinary(t *testing.T, sessionPages map[string]string, usageByAgent map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	for cursor, body := range sessionPages {
		name := cursor
		if name == "" {
			name = "first"
		}
		if err := os.WriteFile(filepath.Join(dir, "session-"+name+".json"), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	for agent, body := range usageByAgent {
		name := agent
		if name == "" {
			name = "all"
		}
		if err := os.WriteFile(filepath.Join(dir, "usage-"+name+".json"), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	const script = `#!/bin/sh
dir="$(dirname "$0")"

if [ "$1" = "session" ]; then
  shift 2 # past "session" "list"
  cursor=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --cursor) cursor="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  if [ -z "$cursor" ]; then
    cat "$dir/session-first.json"
  else
    cat "$dir/session-$cursor.json"
  fi
  exit 0
fi

shift 2 # past "usage" "daily"
agent=""
while [ $# -gt 0 ]; do
  case "$1" in
    --agent) agent="$2"; shift 2 ;;
    --since) shift 2 ;;
    *) shift ;;
  esac
done
if [ -z "$agent" ]; then
  cat "$dir/usage-all.json"
else
  cat "$dir/usage-$agent.json"
fi
`
	path := filepath.Join(dir, "agentsview")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestResolve_HappyPath_TwoAgentsDistinctModels(t *testing.T) {
	bin := fakeMultiAgentBinary(t,
		map[string]string{
			"": `{"sessions": [{"agent": "claude-code"}, {"agent": "codex"}], "next_cursor": ""}`,
		},
		map[string]string{
			"claude-code": `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "claude-sonnet-5", "inputTokens": 60, "outputTokens": 40, "cost": 1.0}]}], "totals": {"inputTokens": 60, "outputTokens": 40, "totalCost": 1.0}}`,
			"codex":       `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "gpt-5.4", "inputTokens": 30, "outputTokens": 20, "cost": 0.5}]}], "totals": {"inputTokens": 30, "outputTokens": 20, "totalCost": 0.5}}`,
		},
	)
	client := &Client{BinaryName: bin}

	ds, err := client.Resolve(t.Context(), ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}

	byModel := ds.ByModel()
	wantByModel := map[string]Totals{
		"claude-sonnet-5": {Tokens: 100, Cost: 1.0},
		"gpt-5.4":         {Tokens: 50, Cost: 0.5},
	}
	if len(byModel) != len(wantByModel) {
		t.Fatalf("ByModel() = %+v, want %+v", byModel, wantByModel)
	}
	for model, want := range wantByModel {
		if got := byModel[model]; got != want {
			t.Errorf("ByModel()[%q] = %+v, want %+v", model, got, want)
		}
	}

	byTool := ds.ByTool()
	wantByTool := map[string]Totals{
		"claude-code": {Tokens: 100, Cost: 1.0},
		"codex":       {Tokens: 50, Cost: 0.5},
	}
	if len(byTool) != len(wantByTool) {
		t.Fatalf("ByTool() = %+v, want %+v", byTool, wantByTool)
	}
	for agent, want := range wantByTool {
		if got := byTool[agent]; got != want {
			t.Errorf("ByTool()[%q] = %+v, want %+v", agent, got, want)
		}
	}
}

// TestResolve_SumMatchesUnfilteredCall validates the KTD3 equivalence
// assumption: summing per-agent FetchUsageDaily calls (what Resolve does)
// must equal what a single unfiltered FetchUsageDaily call reports for the
// same window.
func TestResolve_SumMatchesUnfilteredCall(t *testing.T) {
	// Per-agent filtered calls report the same modelBreakdowns split as the
	// synthetic per-agent bodies below; the unfiltered call's modelBreakdowns
	// aggregate across every agent's usage (real-schema behavior, see
	// DailyRow's doc comment) — here that happens to be one entry per
	// distinct model since each agent used a different model. The
	// comparison below is over Totals (grand sums), not per-row equality,
	// since row-level Agent attribution is opts.Agent-derived and therefore
	// meaningless for the unfiltered call.
	bin := fakeMultiAgentBinary(t,
		map[string]string{
			"": `{"sessions": [{"agent": "claude-code"}, {"agent": "codex"}], "next_cursor": ""}`,
		},
		map[string]string{
			"claude-code": `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "claude-sonnet-5", "inputTokens": 60, "outputTokens": 40, "cost": 1.0}]}], "totals": {"inputTokens": 60, "outputTokens": 40, "totalCost": 1.0}}`,
			"codex":       `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "gpt-5.4", "inputTokens": 30, "outputTokens": 20, "cost": 0.5}]}], "totals": {"inputTokens": 30, "outputTokens": 20, "totalCost": 0.5}}`,
			"":            `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "claude-sonnet-5", "inputTokens": 60, "outputTokens": 40, "cost": 1.0}, {"modelName": "gpt-5.4", "inputTokens": 30, "outputTokens": 20, "cost": 0.5}]}], "totals": {"inputTokens": 90, "outputTokens": 60, "totalCost": 1.5}}`,
		},
	)
	client := &Client{BinaryName: bin}

	ds, err := client.Resolve(t.Context(), ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}

	unfiltered, err := client.FetchUsageDaily(t.Context(), FetchOptions{})
	if err != nil {
		t.Fatalf("FetchUsageDaily() error = %v, want nil", err)
	}

	if got, want := ds.Total(), unfiltered.Totals; got != want {
		t.Errorf("Resolve summed Total() = %+v, want it to equal unfiltered FetchUsageDaily totals %+v", got, want)
	}
}

// TestResolve_ZeroUsageAgentOmitted covers the case where an active agent
// (present in `session list`) reports a zero-value row for the window — it
// must not appear in the resolved Dataset at all, not as a zero row.
func TestResolve_ZeroUsageAgentOmitted(t *testing.T) {
	bin := fakeMultiAgentBinary(t,
		map[string]string{
			"": `{"sessions": [{"agent": "claude-code"}, {"agent": "codex"}], "next_cursor": ""}`,
		},
		map[string]string{
			"claude-code": `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "claude-sonnet-5", "inputTokens": 60, "outputTokens": 40, "cost": 1.0}]}], "totals": {"inputTokens": 60, "outputTokens": 40, "totalCost": 1.0}}`,
			"codex":       `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "gpt-5.4", "inputTokens": 0, "outputTokens": 0, "cost": 0}]}], "totals": {"inputTokens": 0, "outputTokens": 0, "totalCost": 0}}`,
		},
	)
	client := &Client{BinaryName: bin}

	ds, err := client.Resolve(t.Context(), ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}

	if _, ok := ds.ByTool()["codex"]; ok {
		t.Errorf("ByTool() contains a %q entry, want it omitted entirely for a zero-usage agent", "codex")
	}
	for _, r := range ds.Rows {
		if r.Agent == "codex" {
			t.Errorf("Rows contains a zero-usage row for codex = %+v, want it omitted", r)
		}
	}
}

// TestResolve_SameModelAcrossAgents_ByModelSumsByToolSeparates covers the
// case where the same model is used through two different agents: ByModel
// must sum both agents' contributions together, while ByTool must keep each
// agent's contribution separate.
func TestResolve_SameModelAcrossAgents_ByModelSumsByToolSeparates(t *testing.T) {
	bin := fakeMultiAgentBinary(t,
		map[string]string{
			"": `{"sessions": [{"agent": "claude-code"}, {"agent": "codex"}], "next_cursor": ""}`,
		},
		map[string]string{
			"claude-code": `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "gpt-5.4", "inputTokens": 60, "outputTokens": 40, "cost": 1.0}]}], "totals": {"inputTokens": 60, "outputTokens": 40, "totalCost": 1.0}}`,
			"codex":       `{"daily": [{"date": "2026-06-20", "modelBreakdowns": [{"modelName": "gpt-5.4", "inputTokens": 30, "outputTokens": 20, "cost": 0.5}]}], "totals": {"inputTokens": 30, "outputTokens": 20, "totalCost": 0.5}}`,
		},
	)
	client := &Client{BinaryName: bin}

	ds, err := client.Resolve(t.Context(), ResolveOptions{})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}

	if got, want := ds.ByModel()["gpt-5.4"], (Totals{Tokens: 150, Cost: 1.5}); got != want {
		t.Errorf("ByModel()[gpt-5.4] = %+v, want %+v (summed across both agents)", got, want)
	}

	byTool := ds.ByTool()
	if got, want := byTool["claude-code"], (Totals{Tokens: 100, Cost: 1.0}); got != want {
		t.Errorf("ByTool()[claude-code] = %+v, want %+v (kept separate from codex)", got, want)
	}
	if got, want := byTool["codex"], (Totals{Tokens: 50, Cost: 0.5}); got != want {
		t.Errorf("ByTool()[codex] = %+v, want %+v (kept separate from claude-code)", got, want)
	}
}
