package render_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/render"
	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// fixtureDataset is a realistic multi-agent, multi-model, multi-day dataset
// shared across tests. Agent names ("claude-code", "codex") never appear as
// substrings of any model name here, so tests can assert on their presence
// or absence in a breakdown section without ambiguity.
func fixtureDataset() snapshot.MergedDataset {
	return snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 1.50},
		{Date: "2026-06-20", Agent: "codex", Model: "gpt-5.4", Tokens: 500, Cost: 0.75},
		{Date: "2026-06-21", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1200, Cost: 1.80},
		{Date: "2026-06-21", Agent: "codex", Model: "gpt-5.4", Tokens: 600, Cost: 0.90},
		{Date: "2026-06-22", Agent: "claude-code", Model: "claude-opus-5", Tokens: 800, Cost: 2.00},
	}}
}

// TestRender_HappyPath_AllFourBlocksInOrder covers the confirmed
// dashboard-card layout (R8): summary, trend graph, streak, and breakdown
// all appear, in that order, inside a single bordered box.
func TestRender_HappyPath_AllFourBlocksInOrder(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	renderedAt := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)

	out := render.Render(ds, sum, config.BreakdownPerModel, renderedAt)

	if !strings.HasPrefix(strings.TrimRight(out, "\n"), "┌") {
		t.Errorf("Render() output does not start with a top border: %q", firstLine(out))
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "┘") {
		t.Errorf("Render() output does not end with a bottom border: %q", lastLine(out))
	}

	summaryIdx := strings.Index(out, "Tokens:")
	trendIdx := strings.Index(out, "Trend")
	streakIdx := strings.Index(out, "Streak:")
	breakdownIdx := strings.Index(out, "Breakdown")

	if summaryIdx < 0 || trendIdx < 0 || streakIdx < 0 || breakdownIdx < 0 {
		t.Fatalf("Render() output missing a block: summaryIdx=%d trendIdx=%d streakIdx=%d breakdownIdx=%d\noutput:\n%s",
			summaryIdx, trendIdx, streakIdx, breakdownIdx, out)
	}
	if !(summaryIdx < trendIdx && trendIdx < streakIdx && streakIdx < breakdownIdx) {
		t.Errorf("Render() blocks out of order: summary=%d trend=%d streak=%d breakdown=%d",
			summaryIdx, trendIdx, streakIdx, breakdownIdx)
	}
}

// TestRender_DefaultBreakdownIsPerModel covers AE1: an unchanged
// (BreakdownPerModel) config renders a breakdown grouped by model only —
// model names appear, agent names do not.
func TestRender_DefaultBreakdownIsPerModel(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, renderedAt)

	for _, model := range []string{"claude-sonnet-5", "gpt-5.4", "claude-opus-5"} {
		if !strings.Contains(out, model) {
			t.Errorf("Render() output missing model %q in per-model breakdown:\n%s", model, out)
		}
	}
	for _, agent := range []string{"claude-code", "codex"} {
		if strings.Contains(out, agent) {
			t.Errorf("Render() output unexpectedly contains agent name %q in per-model breakdown:\n%s", agent, out)
		}
	}
}

// TestRender_BreakdownModesDiffer covers the edge case where PerTool and
// Combined modes, applied to the same underlying fixture, must each render
// breakdown content distinct from each other and from the PerModel case:
// PerTool groups by agent, Combined collapses to one total line.
func TestRender_BreakdownModesDiffer(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	renderedAt := asOf

	perModel := render.Render(ds, sum, config.BreakdownPerModel, renderedAt)
	perTool := render.Render(ds, sum, config.BreakdownPerTool, renderedAt)
	combined := render.Render(ds, sum, config.BreakdownCombined, renderedAt)

	if perModel == perTool || perModel == combined || perTool == combined {
		t.Fatalf("expected distinct output per breakdown mode, got at least one pair equal")
	}

	for _, agent := range []string{"claude-code", "codex"} {
		if !strings.Contains(perTool, agent) {
			t.Errorf("per-tool breakdown missing agent %q:\n%s", agent, perTool)
		}
	}
	for _, model := range []string{"claude-sonnet-5", "gpt-5.4", "claude-opus-5"} {
		if strings.Contains(perTool, model) {
			t.Errorf("per-tool breakdown unexpectedly contains model name %q:\n%s", model, perTool)
		}
	}

	for _, name := range []string{"claude-code", "codex", "claude-sonnet-5", "gpt-5.4", "claude-opus-5"} {
		if strings.Contains(combined, name) {
			t.Errorf("combined breakdown unexpectedly contains grouping label %q:\n%s", name, combined)
		}
	}
	if !strings.Contains(combined, "4,100") {
		t.Errorf("combined breakdown missing combined token total \"4,100\":\n%s", combined)
	}
}

// TestRender_StaleRenderedAtVisibleAsOldDate covers AE3: a "rendered at"
// timestamp 10+ days in the past must render visibly as that specific old
// date, not be obscured behind a vague/relative freshness indicator.
func TestRender_StaleRenderedAtVisibleAsOldDate(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	staleRenderedAt := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC) // 10 days before asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, staleRenderedAt)

	const want = "2026-06-12 09:00 UTC"
	if !strings.Contains(out, want) {
		t.Errorf("Render() output missing exact stale timestamp %q:\n%s", want, out)
	}
	if strings.Contains(out, "ago") || strings.Contains(out, "just now") {
		t.Errorf("Render() output should not use vague relative phrasing:\n%s", out)
	}
}

// TestRender_EmptyDataset_NoDataYetState covers the brand-new-adopter edge
// case: zero rows must not panic or produce a malformed asciigraph plot
// (Plot panics on an empty series) — the trend and breakdown blocks instead
// render an explicit "no data yet" state.
func TestRender_EmptyDataset_NoDataYetState(t *testing.T) {
	ds := snapshot.MergedDataset{}
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, renderedAt)

	if !strings.Contains(out, "No data yet") {
		t.Errorf("Render() output missing \"No data yet\" state for empty dataset:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimRight(out, "\n"), "┌") || !strings.HasSuffix(strings.TrimRight(out, "\n"), "┘") {
		t.Errorf("Render() output not a well-formed bordered box for empty dataset:\n%s", out)
	}
}

// TestRender_TrendGraphGroupsByDateSummingTokens covers the trend graph's
// data prep (Approach, R5): rows are grouped by date, summing tokens across
// agent/model, sorted chronologically, and fed to asciigraph — not a
// placeholder single-point series. The fixture's three distinct dates
// (compact MM-DD axis labels) must all appear as x-axis ticks.
func TestRender_TrendGraphGroupsByDateSummingTokens(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, renderedAt)

	for _, label := range []string{"06-20", "06-21", "06-22"} {
		if !strings.Contains(out, label) {
			t.Errorf("Render() trend graph missing x-axis date label %q:\n%s", label, out)
		}
	}
	// 2026-06-22's daily total (the fixture's sole claude-opus-5 row, 800
	// tokens) is the series minimum and, as a series endpoint, survives
	// asciigraph's width interpolation exactly — confirms per-date summing
	// reached the plot, not a placeholder series.
	if !strings.Contains(out, "800") {
		t.Errorf("Render() trend graph missing summed daily endpoint \"800\":\n%s", out)
	}
}

// TestRender_GoldenFile locks in the exact rendered layout for a realistic
// fixture, once the behavior tests above have settled the format. Written
// last per the plan, so this test isn't fighting an evolving layout.
func TestRender_GoldenFile(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf)
	renderedAt := time.Date(2026, 6, 22, 14, 30, 0, 0, time.UTC)

	got := render.Render(ds, sum, config.BreakdownPerModel, renderedAt)

	want, err := os.ReadFile("testdata/dashboard_card.golden")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if got != string(want) {
		t.Errorf("Render() output does not match golden file testdata/dashboard_card.golden\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func firstLine(s string) string {
	before, _, _ := strings.Cut(s, "\n")
	return before
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
