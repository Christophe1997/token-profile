package agentsview

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// realDailyClaudeFixture is a real `agentsview usage daily --json --breakdown
// --offline --since 2026-06-25 --agent claude` response, captured from the
// actual agentsview v0.36.0 binary (see internal/agentsview/testdata). It is
// ground truth for the real nested schema, not a hand-fabricated shape.
const realDailyClaudeFixturePath = "testdata/real_usage_daily_claude.json"

func TestParseUsageDaily_RealClaudeFixture(t *testing.T) {
	data, err := os.ReadFile(realDailyClaudeFixturePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	got, err := parseUsageDaily(data, "claude")
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil", err)
	}

	// 7 days, with 3+1+2+2+2+1+3 modelBreakdowns entries flattened to one
	// DailyRow each (real fixture counts, see testdata/real_usage_daily_claude.json).
	const wantRows = 14
	if len(got.Daily) != wantRows {
		t.Fatalf("len(Daily) = %d, want %d", len(got.Daily), wantRows)
	}

	for _, row := range got.Daily {
		if row.Agent != "claude" {
			t.Errorf("row %+v: Agent = %q, want %q (FetchOptions.Agent, not JSON)", row, row.Agent, "claude")
		}
		if row.Tokens <= 0 {
			t.Errorf("row %+v: Tokens = %d, want > 0", row, row.Tokens)
		}
		if row.Cost <= 0 {
			t.Errorf("row %+v: Cost = %v, want > 0", row, row.Cost)
		}
	}

	// Spot-check one specific row against the fixture's raw numbers:
	// 2026-06-25 claude-opus-4-8: inputTokens=951828, outputTokens=718247,
	// cacheReadTokens=151434047 (excluded), cost=121.38727599999999.
	var found bool
	for _, row := range got.Daily {
		if row.Date == "2026-06-25" && row.Model == "claude-opus-4-8" {
			found = true
			const wantTokens = 951828 + 718247 // conversation tokens only, no cache
			const wantCost = 121.38727599999999
			if row.Tokens != wantTokens {
				t.Errorf("2026-06-25/claude-opus-4-8 Tokens = %d, want %d (input+output only, cache excluded)", row.Tokens, wantTokens)
			}
			if row.Cost != wantCost {
				t.Errorf("2026-06-25/claude-opus-4-8 Cost = %v, want %v", row.Cost, wantCost)
			}
		}
	}
	if !found {
		t.Fatal("no flattened row for 2026-06-25/claude-opus-4-8, want one")
	}

	// Sum of flattened row tokens/cost must reconcile with the fixture's
	// top-level totals (inputTokens=7423972, outputTokens=3302262,
	// totalCost=400.4263420999999).
	var sumTokens int64
	var sumCost float64
	for _, row := range got.Daily {
		sumTokens += row.Tokens
		sumCost += row.Cost
	}
	const wantTotalTokens = 7423972 + 3302262
	if sumTokens != wantTotalTokens {
		t.Errorf("sum of row Tokens = %d, want %d (matching fixture totals.inputTokens+outputTokens)", sumTokens, wantTotalTokens)
	}
	const wantTotalCost = 400.4263420999999
	if math.Abs(sumCost-wantTotalCost) > 1e-6 {
		t.Errorf("sum of row Cost = %v, want ~%v (matching fixture totals.totalCost)", sumCost, wantTotalCost)
	}

	if got.Totals.Tokens != wantTotalTokens {
		t.Errorf("Totals.Tokens = %d, want %d", got.Totals.Tokens, wantTotalTokens)
	}
	if got.Totals.Cost != wantTotalCost {
		t.Errorf("Totals.Cost = %v, want %v", got.Totals.Cost, wantTotalCost)
	}
}

func TestParseUsageDaily_FlattensModelBreakdownsPerDay(t *testing.T) {
	const fixture = `{
		"daily": [
			{
				"date": "2026-06-20",
				"modelBreakdowns": [
					{"modelName": "claude-sonnet-5", "inputTokens": 100, "outputTokens": 200, "cacheCreationTokens": 50, "cacheReadTokens": 900, "cost": 1.23},
					{"modelName": "claude-haiku-4-5", "inputTokens": 10, "outputTokens": 20, "cacheCreationTokens": 5, "cacheReadTokens": 90, "cost": 0.05}
				]
			},
			{
				"date": "2026-06-21",
				"modelBreakdowns": [
					{"modelName": "claude-sonnet-5", "inputTokens": 300, "outputTokens": 400, "cacheCreationTokens": 60, "cacheReadTokens": 950, "cost": 2.34}
				]
			}
		],
		"totals": {"inputTokens": 410, "outputTokens": 620, "cacheCreationTokens": 115, "cacheReadTokens": 1940, "totalCost": 3.62}
	}`

	got, err := parseUsageDaily([]byte(fixture), "claude")
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil", err)
	}

	// One DailyRow per (day, model) — 2 on the first day, 1 on the second.
	if len(got.Daily) != 3 {
		t.Fatalf("len(Daily) = %d, want 3", len(got.Daily))
	}

	want := []DailyRow{
		{Date: "2026-06-20", Agent: "claude", Model: "claude-sonnet-5", Tokens: 300, Cost: 1.23},
		{Date: "2026-06-20", Agent: "claude", Model: "claude-haiku-4-5", Tokens: 30, Cost: 0.05},
		{Date: "2026-06-21", Agent: "claude", Model: "claude-sonnet-5", Tokens: 700, Cost: 2.34},
	}
	if !slices.Equal(got.Daily, want) {
		t.Errorf("Daily = %+v, want %+v", got.Daily, want)
	}

	if got.Totals != (Totals{Tokens: 1030, Cost: 3.62}) {
		t.Errorf("Totals = %+v, want {Tokens: 1030, Cost: 3.62}", got.Totals)
	}
}

// TestParseUsageDaily_AttributesAgentFromRequestNotJSON covers the
// deliberate design choice documented on DailyRow: a day's agentBreakdowns
// can list several agents (this happens on an *unfiltered* call, where
// modelBreakdowns aggregates across every agent's usage of that model), so
// there is no way to attribute one modelBreakdown entry to one agent from
// the JSON alone. Every flattened row must instead take its Agent from the
// FetchOptions.Agent the call was made with.
func TestParseUsageDaily_AttributesAgentFromRequestNotJSON(t *testing.T) {
	const fixture = `{
		"daily": [
			{
				"date": "2026-06-20",
				"modelBreakdowns": [
					{"modelName": "claude-sonnet-5", "inputTokens": 100, "outputTokens": 200, "cost": 1.0}
				],
				"agentBreakdowns": [
					{"agent": "claude", "inputTokens": 60, "outputTokens": 120, "cost": 0.6},
					{"agent": "codex", "inputTokens": 40, "outputTokens": 80, "cost": 0.4}
				]
			}
		],
		"totals": {"inputTokens": 100, "outputTokens": 200, "totalCost": 1.0}
	}`

	got, err := parseUsageDaily([]byte(fixture), "requested-agent")
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil", err)
	}

	if len(got.Daily) != 1 {
		t.Fatalf("len(Daily) = %d, want 1", len(got.Daily))
	}
	if got.Daily[0].Agent != "requested-agent" {
		t.Errorf("Daily[0].Agent = %q, want %q (attributed from the call's requested agent, ignoring agentBreakdowns)", got.Daily[0].Agent, "requested-agent")
	}
}

// TestParseUsageDaily_TokensExcludeCacheTokens covers the deliberate design
// choice documented on DailyRow: Tokens counts only
// inputTokens+outputTokens. Real usage data can have cacheReadTokens ~100x
// larger than inputTokens, and folding those in would make the headline
// "tokens used" number reflect cache mechanics rather than actual work.
func TestParseUsageDaily_TokensExcludeCacheTokens(t *testing.T) {
	const fixture = `{
		"daily": [
			{
				"date": "2026-06-20",
				"modelBreakdowns": [
					{"modelName": "claude-opus-4-8", "inputTokens": 1000, "outputTokens": 500, "cacheCreationTokens": 200000, "cacheReadTokens": 90000000, "cost": 12.5}
				]
			}
		],
		"totals": {"inputTokens": 1000, "outputTokens": 500, "cacheCreationTokens": 200000, "cacheReadTokens": 90000000, "totalCost": 12.5}
	}`

	got, err := parseUsageDaily([]byte(fixture), "claude")
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil", err)
	}

	const wantTokens = 1000 + 500 // NOT +200000+90000000
	if got.Daily[0].Tokens != wantTokens {
		t.Errorf("Daily[0].Tokens = %d, want %d (cache tokens must be excluded)", got.Daily[0].Tokens, wantTokens)
	}
	if got.Totals.Tokens != wantTokens {
		t.Errorf("Totals.Tokens = %d, want %d (cache tokens must be excluded)", got.Totals.Tokens, wantTokens)
	}
}

func TestParseUsageDaily_IgnoresUnrecognizedFields(t *testing.T) {
	const fixture = `{
		"daily": [
			{
				"date": "2026-06-20",
				"modelBreakdowns": [
					{"modelName": "claude-sonnet-5", "inputTokens": 100, "outputTokens": 200, "cost": 1.23, "newField": "surprise"}
				],
				"projectBreakdowns": [{"project": "foo", "inputTokens": 100}],
				"modelsUsed": ["claude-sonnet-5"]
			}
		],
		"totals": {"inputTokens": 100, "outputTokens": 200, "totalCost": 1.23, "cacheSavings": 9.99, "anotherNewField": {"nested": true}},
		"sessionCounts": {"total": 1},
		"schemaVersion": "2.0"
	}`

	got, err := parseUsageDaily([]byte(fixture), "claude")
	if err != nil {
		t.Fatalf("parseUsageDaily() error = %v, want nil (unknown fields must be ignored)", err)
	}

	if len(got.Daily) != 1 || got.Daily[0].Tokens != 300 {
		t.Errorf("Daily = %+v, want one row with tokens=300", got.Daily)
	}
	if got.Totals.Tokens != 300 {
		t.Errorf("Totals.Tokens = %d, want 300", got.Totals.Tokens)
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

// TestFetchUsageDaily_PassesUTCTimezoneFlag covers KTD5: usage dates must be
// bucketed in UTC, not agentsview's local-system-timezone default, so every
// FetchUsageDaily invocation must pass --timezone UTC.
func TestFetchUsageDaily_PassesUTCTimezoneFlag(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	bin := fakeAgentsview(t, `echo "$@" > `+argsFile+`
echo '{"daily": [], "totals": {}}'
`)
	client := &Client{BinaryName: bin}

	if _, err := client.FetchUsageDaily(t.Context(), FetchOptions{Agent: "claude"}); err != nil {
		t.Fatalf("FetchUsageDaily() error = %v, want nil", err)
	}

	gotArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(gotArgs), "--timezone UTC") {
		t.Errorf("FetchUsageDaily args = %q, want them to contain --timezone UTC", gotArgs)
	}
}

func TestListActiveAgents_SinglePage_ReturnsDistinctSortedAgents(t *testing.T) {
	bin := fakeAgentsview(t, `cat <<'EOF'
{"sessions": [{"agent": "codex"}, {"agent": "claude-code"}, {"agent": "codex"}], "next_cursor": ""}
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
{"sessions": [{"agent": "codex"}, {"agent": "gemini-cli"}], "next_cursor": ""}
EOF
    ;;
  *)
    cat <<'EOF'
{"sessions": [{"agent": "claude-code"}, {"agent": "codex"}], "next_cursor": "page2"}
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
		t.Errorf("ListActiveAgents() = %v, want %v (pagination should follow next_cursor until exhausted)", got, want)
	}
}

// TestSessionListResponse_DecodesRealNextCursorField decodes a real
// `agentsview session list --json` response (trimmed to a handful of
// sessions, see testdata/real_session_list_trimmed.json) and confirms
// next_cursor — the real API's snake_case field name — is read correctly.
// The untrimmed capture had total=233 sessions across a 200-session page,
// so this fixture's next_cursor is a real, non-empty continuation token.
func TestSessionListResponse_DecodesRealNextCursorField(t *testing.T) {
	data, err := os.ReadFile("testdata/real_session_list_trimmed.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var resp sessionListResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}

	if resp.NextCursor == "" {
		t.Fatal("NextCursor = \"\", want the fixture's real non-empty continuation token")
	}
	if !strings.HasPrefix(resp.NextCursor, "eyJ") {
		t.Errorf("NextCursor = %q, want it to match the fixture's captured token", resp.NextCursor)
	}
	if len(resp.Sessions) == 0 {
		t.Fatal("Sessions is empty, want at least one session from the fixture")
	}
	if resp.Sessions[0].Agent != "claude" {
		t.Errorf("Sessions[0].Agent = %q, want %q", resp.Sessions[0].Agent, "claude")
	}
}
