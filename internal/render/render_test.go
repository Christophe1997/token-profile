package render_test

import (
	"os"
	"regexp"
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
// dashboard-card layout (R8): a title, summary, trend graph, streak, and
// breakdown all appear, in that order, inside a single bordered box.
func TestRender_HappyPath_AllFourBlocksInOrder(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

	if !strings.HasPrefix(strings.TrimRight(out, "\n"), "┌") {
		t.Errorf("Render() output does not start with a top border: %q", firstLine(out))
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "┘") {
		t.Errorf("Render() output does not end with a bottom border: %q", lastLine(out))
	}

	titleIdx := strings.Index(out, "Token Profile")
	summaryIdx := strings.Index(out, "Tokens:")
	trendIdx := strings.Index(out, "Trend")
	streakIdx := strings.Index(out, "Streak:")
	breakdownIdx := strings.Index(out, "Breakdown")

	if titleIdx < 0 || summaryIdx < 0 || trendIdx < 0 || streakIdx < 0 || breakdownIdx < 0 {
		t.Fatalf("Render() output missing a block: titleIdx=%d summaryIdx=%d trendIdx=%d streakIdx=%d breakdownIdx=%d\noutput:\n%s",
			titleIdx, summaryIdx, trendIdx, streakIdx, breakdownIdx, out)
	}
	if !(titleIdx < summaryIdx && summaryIdx < trendIdx && trendIdx < streakIdx && streakIdx < breakdownIdx) {
		t.Errorf("Render() blocks out of order: title=%d summary=%d trend=%d streak=%d breakdown=%d",
			titleIdx, summaryIdx, trendIdx, streakIdx, breakdownIdx)
	}
}

// TestRender_TitleIsFirstContentLine covers the card's title (R8): it is
// the box's very first content line, immediately below the top border, so
// the card identifies itself before any usage data.
func TestRender_TitleIsFirstContentLine(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("Render() output has too few lines: %q", out)
	}
	if !strings.Contains(lines[1], "Token Profile") {
		t.Errorf("Render() first content line = %q, want it to contain \"Token Profile\"", lines[1])
	}
}

// TestRender_TitleIncludesStatDuration covers the title's stat-duration
// suffix: the inclusive calendar span between the dataset's earliest and
// latest recorded day, e.g. three distinct days (06-20 through 06-22) reads
// "last 3 days", and a single day is singular ("last 1 day"). An empty
// dataset has no span to report, so the title omits the suffix entirely.
func TestRender_TitleIncludesStatDuration(t *testing.T) {
	tests := []struct {
		name string
		ds   snapshot.MergedDataset
		want string
	}{
		{
			name: "multi-day span",
			ds:   fixtureDataset(),
			want: "Token Profile — last 3 days",
		},
		{
			name: "single day is singular",
			ds: snapshot.MergedDataset{Rows: []snapshot.Row{
				{Date: "2026-07-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
			}},
			want: "Token Profile — last 1 day",
		},
		{
			name: "empty dataset omits duration",
			ds:   snapshot.MergedDataset{},
			want: "Token Profile",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
			sum := summary.Compute(tt.ds, asOf, 30*24*time.Hour)
			out := render.Render(tt.ds, sum, config.BreakdownPerModel, -1, asOf)

			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) < 2 {
				t.Fatalf("Render() output has too few lines: %q", out)
			}
			got := strings.TrimSuffix(strings.TrimPrefix(lines[1], "│ "), " │")
			got = strings.TrimRight(got, " ")
			if got != tt.want {
				t.Errorf("Render() title line = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRender_DefaultBreakdownIsPerModel covers AE1: an unchanged
// (BreakdownPerModel) config renders a breakdown grouped by model only —
// model names appear, agent names do not.
func TestRender_DefaultBreakdownIsPerModel(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

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
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := asOf

	perModel := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)
	perTool := render.Render(ds, sum, config.BreakdownPerTool, -1, renderedAt)
	combined := render.Render(ds, sum, config.BreakdownCombined, -1, renderedAt)

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

// manyModelsDataset has 5 distinct models with distinct, easily orderable
// token totals, for exercising breakdown-limit truncation (fixtureDataset's
// 3 models can't exercise a cap below its own count).
func manyModelsDataset() snapshot.MergedDataset {
	return snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "model-a", Tokens: 500, Cost: 5.00},
		{Date: "2026-06-20", Agent: "claude-code", Model: "model-b", Tokens: 400, Cost: 4.00},
		{Date: "2026-06-20", Agent: "claude-code", Model: "model-c", Tokens: 300, Cost: 3.00},
		{Date: "2026-06-20", Agent: "claude-code", Model: "model-d", Tokens: 200, Cost: 2.00},
		{Date: "2026-06-20", Agent: "claude-code", Model: "model-e", Tokens: 100, Cost: 1.00},
	}}
}

// TestRender_BreakdownLimit_TruncatesToTopN covers the default top-N
// display: a positive limit shows only the highest-token entries up to
// that count, summarizing the rest in one combined line rather than
// dropping them silently.
func TestRender_BreakdownLimit_TruncatesToTopN(t *testing.T) {
	ds := manyModelsDataset()
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	out := render.Render(ds, sum, config.BreakdownPerModel, 3, asOf)

	for _, model := range []string{"model-a", "model-b", "model-c"} {
		if !strings.Contains(out, model) {
			t.Errorf("Render() missing top model %q:\n%s", model, out)
		}
	}
	for _, model := range []string{"model-d", "model-e"} {
		if strings.Contains(out, model) {
			t.Errorf("Render() unexpectedly shows omitted model %q individually:\n%s", model, out)
		}
	}
	if !strings.Contains(out, "2 more") {
		t.Errorf("Render() missing an omitted-entries summary line (\"2 more\"):\n%s", out)
	}
	// model-d (200 tokens, $2.00) + model-e (100 tokens, $1.00)
	if !strings.Contains(out, "300") || !strings.Contains(out, "$3.00") {
		t.Errorf("Render() omitted-entries summary missing combined totals (300 tokens, $3.00):\n%s", out)
	}
}

// TestRender_BreakdownLimit_NonPositiveShowsEveryEntry covers the
// "unlimited" sentinel: a zero or negative limit must show every entry
// with no summary line, matching pre-limit behavior.
func TestRender_BreakdownLimit_NonPositiveShowsEveryEntry(t *testing.T) {
	ds := manyModelsDataset()
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	for _, limit := range []int{0, -1} {
		out := render.Render(ds, sum, config.BreakdownPerModel, limit, asOf)
		for _, model := range []string{"model-a", "model-b", "model-c", "model-d", "model-e"} {
			if !strings.Contains(out, model) {
				t.Errorf("Render(limit=%d) missing model %q, want every entry shown:\n%s", limit, model, out)
			}
		}
		if strings.Contains(out, "more") {
			t.Errorf("Render(limit=%d) unexpectedly shows an omitted-entries summary line:\n%s", limit, out)
		}
	}
}

// TestRender_BreakdownLimit_AtOrAboveEntryCount_NoSummaryLine covers the
// boundary: a limit equal to (or greater than) the entry count must show
// every entry without an omitted-entries summary line.
func TestRender_BreakdownLimit_AtOrAboveEntryCount_NoSummaryLine(t *testing.T) {
	ds := manyModelsDataset()
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	for _, limit := range []int{5, 10} {
		out := render.Render(ds, sum, config.BreakdownPerModel, limit, asOf)
		if strings.Contains(out, "more") {
			t.Errorf("Render(limit=%d) unexpectedly shows an omitted-entries summary line:\n%s", limit, out)
		}
	}
}

// TestRender_StaleRenderedAtVisibleAsOldDate covers AE3: a "rendered at"
// timestamp 10+ days in the past must render visibly as that specific old
// date, not be obscured behind a vague/relative freshness indicator.
func TestRender_StaleRenderedAtVisibleAsOldDate(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	staleRenderedAt := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC) // 10 days before asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, staleRenderedAt)

	const want = "2026-06-12 09:00 UTC"
	if !strings.Contains(out, want) {
		t.Errorf("Render() output missing exact stale timestamp %q:\n%s", want, out)
	}
	if strings.Contains(out, "ago") || strings.Contains(out, "just now") {
		t.Errorf("Render() output should not use vague relative phrasing:\n%s", out)
	}
}

// TestGeneratedByLine_IsMarkdownLinkToRepo covers attribution: it's a
// standalone markdown link back to token-profile's repo, deliberately kept
// out of Render's own boxed output (see GeneratedByLine's doc comment) so
// callers can place it outside the fenced code block a README injects the
// card into, where CommonMark actually renders it as a clickable link.
func TestGeneratedByLine_IsMarkdownLinkToRepo(t *testing.T) {
	const want = "Generated by [token-profile](https://github.com/Christophe1997/token-profile)"
	if got := render.GeneratedByLine(); got != want {
		t.Errorf("GeneratedByLine() = %q, want %q", got, want)
	}
}

// TestRender_EmptyDataset_NoDataYetState covers the brand-new-adopter edge
// case: zero rows must not panic or produce a malformed asciigraph plot
// (Plot panics on an empty series) — the trend and breakdown blocks instead
// render an explicit "no data yet" state.
func TestRender_EmptyDataset_NoDataYetState(t *testing.T) {
	ds := snapshot.MergedDataset{}
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

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
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

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

// TestRender_TrendYAxisUsesTokenUnits covers the trend graph's y-axis: once
// cache tokens are folded into Tokens (see agentsview.DailyRow), daily
// totals routinely land in the millions, so the y-axis must render through
// the same formatTokens shortening as the headline and breakdown lines
// rather than printing raw 7+-digit values.
func TestRender_TrendYAxisUsesTokenUnits(t *testing.T) {
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 5_000_000, Cost: 10},
		{Date: "2026-06-21", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 12_000_000, Cost: 20},
		{Date: "2026-06-22", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 8_000_000, Cost: 15},
	}}
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := asOf

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

	trendIdx := strings.Index(out, "Trend:")
	streakIdx := strings.Index(out, "Streak:")
	if trendIdx < 0 || streakIdx < 0 || streakIdx < trendIdx {
		t.Fatalf("Render() missing Trend/Streak blocks in expected order:\n%s", out)
	}
	trendBlock := out[trendIdx:streakIdx]

	if !strings.Contains(trendBlock, "M") {
		t.Errorf("Render() trend graph y-axis missing an \"M\" unit suffix:\n%s", trendBlock)
	}
	if raw := regexp.MustCompile(`\d{7,}`).FindString(trendBlock); raw != "" {
		t.Errorf("Render() trend graph y-axis shows unshortened raw number %q:\n%s", raw, trendBlock)
	}
}

// TestRender_SingleDayDataset_NoDuplicateAxisLabel covers a brand-new
// adopter's first tracked day: exactly one distinct date must not confuse
// asciigraph's tick placement into printing the same date label twice along
// the x-axis (a degenerate zero-width XAxisRange with tickCount forced to
// 2 with only one real point to place them at).
func TestRender_SingleDayDataset_NoDuplicateAxisLabel(t *testing.T) {
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-07-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
	}}
	asOf := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	// renderedAt deliberately differs from the dataset's sole date, so the
	// "Last updated" line's own (legitimately different) date can't be
	// mistaken for a second trend-graph occurrence of "07-01".
	renderedAt := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)

	out := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

	if got := strings.Count(out, "07-01"); got != 1 {
		t.Errorf("Render() trend graph shows date label \"07-01\" %d times, want exactly 1:\n%s", got, out)
	}
}

// TestRender_GoldenFile locks in the exact rendered layout for a realistic
// fixture, once the behavior tests above have settled the format. Written
// last per the plan, so this test isn't fighting an evolving layout.
func TestRender_GoldenFile(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := time.Date(2026, 6, 22, 14, 30, 0, 0, time.UTC)

	got := render.Render(ds, sum, config.BreakdownPerModel, -1, renderedAt)

	want, err := os.ReadFile("testdata/dashboard_card.golden")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if got != string(want) {
		t.Errorf("Render() output does not match golden file testdata/dashboard_card.golden\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestHeadline_FormatsTokensCostAndStreak covers the plain-text confirmation
// line's happy path: tokens, cost, and a plural-day streak, in that order.
func TestHeadline_FormatsTokensCostAndStreak(t *testing.T) {
	sum := summary.Summary{TotalTokens: 4100, TotalCost: 6.95, Streak: 3}

	got := render.Headline(sum)

	const want = "Tokens: 4,100   Cost: $6.95   Streak: 3 days"
	if got != want {
		t.Errorf("Headline() = %q, want %q", got, want)
	}
}

// TestHeadline_SingularDayStreak covers the singular/plural edge case: a
// one-day streak must say "day", not "days".
func TestHeadline_SingularDayStreak(t *testing.T) {
	sum := summary.Summary{TotalTokens: 100, TotalCost: 1.0, Streak: 1}

	got := render.Headline(sum)

	if !strings.HasSuffix(got, "Streak: 1 day") {
		t.Errorf("Headline() = %q, want suffix %q", got, "Streak: 1 day")
	}
}

// TestHeadline_ShowsWindowOverWindowChange covers the change-percentage
// suffix: when Summary carries a window-over-window rate, it renders as a
// signed, parenthesized percentage immediately after its total — positive
// rates get an explicit "+", negative rates keep their own "-".
func TestHeadline_ShowsWindowOverWindowChange(t *testing.T) {
	tokenPct, costPct := 50.0, -12.0
	sum := summary.Summary{
		TotalTokens: 4100, TotalCost: 6.95, Streak: 3,
		TokenChangePct: &tokenPct, CostChangePct: &costPct,
	}

	got := render.Headline(sum)

	const want = "Tokens: 4,100 (+50%)   Cost: $6.95 (-12%)   Streak: 3 days"
	if got != want {
		t.Errorf("Headline() = %q, want %q", got, want)
	}
}

// TestHeadline_OmitsChangeSuffixWhenNil covers the no-prior-window case:
// nil TokenChangePct/CostChangePct must render with no suffix at all,
// matching TestHeadline_FormatsTokensCostAndStreak's existing expectation.
func TestHeadline_OmitsChangeSuffixWhenNil(t *testing.T) {
	sum := summary.Summary{TotalTokens: 4100, TotalCost: 6.95, Streak: 3}

	got := render.Headline(sum)

	const want = "Tokens: 4,100   Cost: $6.95   Streak: 3 days"
	if got != want {
		t.Errorf("Headline() = %q, want %q", got, want)
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
