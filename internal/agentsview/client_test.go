package agentsview

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseUsageDaily_HappyPath(t *testing.T) {
	const fixture = `{
		"daily": [
			{"date": "2026-06-20", "agent": "claude-code", "model": "claude-sonnet-5", "tokens": 12345, "cost": 1.23},
			{"date": "2026-06-21", "agent": "codex", "model": "gpt-5.4", "tokens": 6789, "cost": 0.45}
		],
		"totals": {"tokens": 19134, "cost": 1.68}
	}`

	got, err := parseUsageDaily([]byte(fixture))
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil", err)
	}

	if len(got.Daily) != 2 {
		t.Fatalf("len(Daily) = %d, want 2", len(got.Daily))
	}

	row := got.Daily[0]
	if row.Date != "2026-06-20" || row.Agent != "claude-code" || row.Model != "claude-sonnet-5" || row.Tokens != 12345 || row.Cost != 1.23 {
		t.Errorf("Daily[0] = %+v, want date=2026-06-20 agent=claude-code model=claude-sonnet-5 tokens=12345 cost=1.23", row)
	}

	if got.Totals.Tokens != 19134 || got.Totals.Cost != 1.68 {
		t.Errorf("Totals = %+v, want tokens=19134 cost=1.68", got.Totals)
	}
}

func TestParseUsageDaily_IgnoresUnrecognizedFields(t *testing.T) {
	const fixture = `{
		"daily": [
			{"date": "2026-06-20", "agent": "claude-code", "model": "claude-sonnet-5", "tokens": 12345, "cost": 1.23, "newField": "surprise"}
		],
		"totals": {"tokens": 12345, "cost": 1.23, "anotherNewField": {"nested": true}},
		"schemaVersion": "2.0"
	}`

	got, err := parseUsageDaily([]byte(fixture))
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil (unknown fields must be ignored)", err)
	}

	if len(got.Daily) != 1 || got.Daily[0].Tokens != 12345 {
		t.Errorf("Daily = %+v, want one row with tokens=12345", got.Daily)
	}
	if got.Totals.Tokens != 12345 {
		t.Errorf("Totals.Tokens = %d, want 12345", got.Totals.Tokens)
	}
}

func TestFetchUsageDaily_BinaryNotOnPATH_ReturnsNotInstalledError(t *testing.T) {
	client := &Client{BinaryName: "token-profile-agentsview-does-not-exist"}

	_, err := client.FetchUsageDaily(t.Context(), FetchOptions{})
	if err == nil {
		t.Fatal("FetchUsageDaily() error = nil, want a not-installed error")
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("FetchUsageDaily() error = %v, want it to wrap ErrNotInstalled", err)
	}
}

// fakeAgentsview writes an executable shell script fixture standing in for
// the real agentsview binary, returning its absolute path.
func fakeAgentsview(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentsview")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestFetchUsageDaily_NonZeroExit_SurfacesStderr(t *testing.T) {
	bin := fakeAgentsview(t, `echo "rate limit exceeded: retry after 30s" >&2
exit 1
`)
	client := &Client{BinaryName: bin}

	_, err := client.FetchUsageDaily(t.Context(), FetchOptions{})
	if err == nil {
		t.Fatal("FetchUsageDaily() error = nil, want a non-zero-exit error")
	}

	exitErr, ok := errors.AsType[*ExitError](err)
	if !ok {
		t.Fatalf("FetchUsageDaily() error type = %T, want *ExitError", err)
	}
	if !strings.Contains(exitErr.Stderr, "rate limit exceeded") {
		t.Errorf("ExitError.Stderr = %q, want it to contain the fixture's stderr output", exitErr.Stderr)
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("err.Error() = %q, want it to surface stderr content", err.Error())
	}
}

func TestListActiveAgents_SinglePage_ReturnsDistinctSortedAgents(t *testing.T) {
	bin := fakeAgentsview(t, `cat <<'EOF'
{"sessions": [{"agent": "codex"}, {"agent": "claude-code"}, {"agent": "codex"}], "nextCursor": ""}
EOF
`)
	client := &Client{BinaryName: bin}

	got, err := client.ListActiveAgents(t.Context())
	if err != nil {
		t.Fatalf("ListActiveAgents() error = %v, want nil", err)
	}

	want := []string{"claude-code", "codex"}
	if !slices.Equal(got, want) {
		t.Errorf("ListActiveAgents() = %v, want %v", got, want)
	}
}

func TestListActiveAgents_PaginatesAcrossMultiplePages(t *testing.T) {
	// Simulates a >500-session result split across two pages via cursor,
	// with an overlapping "codex" agent on both pages to verify dedup holds
	// across page boundaries too.
	bin := fakeAgentsview(t, `case "$*" in
  *"--cursor page2"*)
    cat <<'EOF'
{"sessions": [{"agent": "codex"}, {"agent": "gemini-cli"}], "nextCursor": ""}
EOF
    ;;
  *)
    cat <<'EOF'
{"sessions": [{"agent": "claude-code"}, {"agent": "codex"}], "nextCursor": "page2"}
EOF
    ;;
esac
`)
	client := &Client{BinaryName: bin}

	got, err := client.ListActiveAgents(t.Context())
	if err != nil {
		t.Fatalf("ListActiveAgents() error = %v, want nil", err)
	}

	want := []string{"claude-code", "codex", "gemini-cli"}
	if !slices.Equal(got, want) {
		t.Errorf("ListActiveAgents() = %v, want %v (pagination should follow nextCursor until exhausted)", got, want)
	}
}
