package render_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/render"
	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// TestRenderSVG_HappyPath_ContainsAllCardBlocks covers R1/R3: a realistic
// multi-day, multi-model fixture renders light and dark SVGs, each
// containing the title, stat values with deltas, a trend polyline, the
// streak, and every breakdown entry — the same information the ASCII card
// shows, just as an SVG image.
func TestRenderSVG_HappyPath_ContainsAllCardBlocks(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := time.Date(2026, 6, 22, 14, 30, 0, 0, time.UTC)

	light, dark, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, renderedAt)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	for _, out := range []string{light, dark} {
		if !strings.HasPrefix(strings.TrimSpace(out), "<svg") {
			t.Errorf("RenderSVG() output does not start with an <svg> element: %q", firstLine(out))
		}
		if !strings.Contains(out, "viewBox=") {
			t.Errorf("RenderSVG() output missing viewBox attribute (needed for responsive scaling):\n%s", out)
		}
		if !strings.Contains(out, "Token Profile") {
			t.Errorf("RenderSVG() output missing title:\n%s", out)
		}
		if !strings.Contains(out, "4,100") {
			t.Errorf("RenderSVG() output missing token total \"4,100\":\n%s", out)
		}
		if !strings.Contains(out, "$6.95") {
			t.Errorf("RenderSVG() output missing cost total \"$6.95\":\n%s", out)
		}
		if !strings.Contains(out, "polyline") {
			t.Errorf("RenderSVG() output missing a trend polyline:\n%s", out)
		}
		if !strings.Contains(out, "Streak: 3 days") {
			t.Errorf("RenderSVG() output missing streak line:\n%s", out)
		}
		for _, model := range []string{"claude-sonnet-5", "gpt-5.4", "claude-opus-5"} {
			if !strings.Contains(out, model) {
				t.Errorf("RenderSVG() output missing breakdown entry %q:\n%s", model, out)
			}
		}
	}

	if light == dark {
		t.Error("RenderSVG() light and dark output must differ")
	}
}

// TestRenderSVG_HappyPath_ShowsWindowOverWindowDeltas covers R1: when Summary
// carries a window-over-window rate, the rendered stat carries a signed
// percentage delta alongside its value.
func TestRenderSVG_HappyPath_ShowsWindowOverWindowDeltas(t *testing.T) {
	tokenPct, costPct := 50.0, -12.0
	sum := summary.Summary{
		TotalTokens: 4100, TotalCost: 6.95, Streak: 3,
		TokenChangePct: &tokenPct, CostChangePct: &costPct,
	}
	ds := fixtureDataset()
	renderedAt := time.Date(2026, 6, 22, 14, 30, 0, 0, time.UTC)

	light, _, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, renderedAt)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	if !strings.Contains(light, "+50%") {
		t.Errorf("RenderSVG() output missing token delta \"+50%%\":\n%s", light)
	}
	if !strings.Contains(light, "-12%") {
		t.Errorf("RenderSVG() output missing cost delta \"-12%%\":\n%s", light)
	}
}

// TestRenderSVG_EmptyDataset_NoDataYetVariant covers the brand-new-adopter
// edge case: zero rows must render a "no data yet" card variant rather than
// an empty or malformed chart, and must not error.
func TestRenderSVG_EmptyDataset_NoDataYetVariant(t *testing.T) {
	ds := snapshot.MergedDataset{}
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	light, dark, err := render.RenderSVG(ds, false, sum, config.BreakdownPerModel, -1, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	for _, out := range []string{light, dark} {
		if !strings.Contains(out, "No data yet") {
			t.Errorf("RenderSVG() output missing \"No data yet\" state for empty dataset:\n%s", out)
		}
		if strings.Contains(out, "polyline") {
			t.Errorf("RenderSVG() output unexpectedly contains a trend polyline for an empty dataset:\n%s", out)
		}
		if !strings.HasPrefix(strings.TrimSpace(out), "<svg") || !strings.Contains(out, "</svg>") {
			t.Errorf("RenderSVG() output not a well-formed SVG for empty dataset:\n%s", out)
		}
	}
}

// TestRenderSVG_InactiveWindowWithHistory_ShowsRealStatsNotNoDataYet covers
// code review F3: a user with real merged history but zero rows in the
// current trailing window (e.g. inactive for 30+ days) must see their real
// headline stats/streak, not the brand-new "no data yet" state — hasHistory
// (derived from the pre-window-filter merged dataset) distinguishes this
// from TestRenderSVG_EmptyDataset_NoDataYetVariant's genuinely-new-adopter
// case, even though ds itself (the current window) is empty in both.
func TestRenderSVG_InactiveWindowWithHistory_ShowsRealStatsNotNoDataYet(t *testing.T) {
	ds := snapshot.MergedDataset{} // current window: empty
	sum := summary.Summary{TotalTokens: 2000, TotalCost: 3.0, Streak: 0}
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	light, dark, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	for _, out := range []string{light, dark} {
		if strings.Contains(out, "No data yet") {
			t.Errorf("RenderSVG() shows the brand-new \"No data yet\" state despite hasHistory=true:\n%s", out)
		}
		if !strings.Contains(out, "2,000") {
			t.Errorf("RenderSVG() missing real token total \"2,000\" from history outside the current window:\n%s", out)
		}
		if !strings.Contains(out, "$3.00") {
			t.Errorf("RenderSVG() missing real cost total \"$3.00\":\n%s", out)
		}
		if strings.Contains(out, "polyline") {
			t.Errorf("RenderSVG() unexpectedly contains a trend polyline for an empty current window:\n%s", out)
		}
		if !strings.Contains(out, "No usage in this window") {
			t.Errorf("RenderSVG() missing a window-scoped (not brand-new) no-data message for the trend/breakdown sections:\n%s", out)
		}
	}
}

// TestRenderSVG_SingleDayDataset_DegenerateChartNoError covers a brand-new
// adopter's first tracked day: exactly one distinct date must render a
// degenerate (single-point) chart without erroring or panicking.
func TestRenderSVG_SingleDayDataset_DegenerateChartNoError(t *testing.T) {
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-07-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
	}}
	asOf := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)

	light, dark, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, renderedAt)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	for _, out := range []string{light, dark} {
		if !strings.Contains(out, "07-01") {
			t.Errorf("RenderSVG() single-day output missing date label \"07-01\":\n%s", out)
		}
		if !strings.Contains(out, "100") {
			t.Errorf("RenderSVG() single-day output missing token value \"100\":\n%s", out)
		}
	}
}

// manyModelsDataset is defined in render_test.go and shared across both
// test files in this package.

// TestRenderSVG_BreakdownLimit_TruncatesToTopNPlusSummary covers the
// default top-N display: a positive limit within the SVG layout's row
// budget shows only the highest-token entries up to that count, folding
// the rest into one "N more" summary line.
func TestRenderSVG_BreakdownLimit_TruncatesToTopNPlusSummary(t *testing.T) {
	ds := manyModelsDataset()
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	light, _, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, 3, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	for _, model := range []string{"model-a", "model-b", "model-c"} {
		if !strings.Contains(light, model) {
			t.Errorf("RenderSVG() missing top model %q:\n%s", model, light)
		}
	}
	for _, model := range []string{"model-d", "model-e"} {
		if strings.Contains(light, model) {
			t.Errorf("RenderSVG() unexpectedly shows omitted model %q individually:\n%s", model, light)
		}
	}
	if !strings.Contains(light, "2 more") {
		t.Errorf("RenderSVG() missing an omitted-entries summary line (\"2 more\"):\n%s", light)
	}
}

// TestRenderSVG_BreakdownLimit_UnlimitedClampsToLayoutBudget covers the
// fixed-canvas overflow risk (Risks & Dependencies): unlike the ASCII
// card's auto-sizing box, an "unlimited" (<=0) breakdown limit must not
// overflow the SVG's fixed vertical layout — it's clamped to however many
// rows the layout can actually fit, with the remainder folded into a
// summary line rather than silently overflowing or being dropped.
func TestRenderSVG_BreakdownLimit_UnlimitedClampsToLayoutBudget(t *testing.T) {
	ds := manyModelsDataset() // 5 distinct models
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	light, _, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	if !strings.Contains(light, "more") {
		t.Errorf("RenderSVG() with 5 entries and an unlimited breakdown limit must still summarize omitted entries to respect the fixed canvas:\n%s", light)
	}
}

// TestRenderSVG_BreakdownEntry_LongLabelTruncatedWithEllipsis covers the
// fixed-canvas overflow risk for a single outlier entry: a model/agent name
// far longer than the layout's breakdown column must be truncated with an
// ellipsis rather than overflowing the card.
func TestRenderSVG_BreakdownEntry_LongLabelTruncatedWithEllipsis(t *testing.T) {
	longName := "an-extremely-long-model-name-that-would-otherwise-overflow-the-fixed-card-layout-if-left-untruncated"
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: longName, Tokens: 500, Cost: 5.0},
	}}
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	light, _, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	if strings.Contains(light, longName) {
		t.Errorf("RenderSVG() did not truncate an outlier-length breakdown label:\n%s", light)
	}
	if !strings.Contains(light, "…") {
		t.Errorf("RenderSVG() truncated label missing an ellipsis marker:\n%s", light)
	}
}

// TestRenderSVG_BreakdownLimit_OversizedPositiveClampsToLayoutBudget covers
// effectiveBreakdownLimit's other clamp disjunct: an explicit positive limit
// larger than the SVG layout's row budget must clamp the same way the
// unlimited (<=0) case does, not just fail to truncate.
func TestRenderSVG_BreakdownLimit_OversizedPositiveClampsToLayoutBudget(t *testing.T) {
	ds := manyModelsDataset() // 5 distinct models
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	light, _, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, 10, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	for _, model := range []string{"model-a", "model-b", "model-c", "model-d"} {
		if !strings.Contains(light, model) {
			t.Errorf("RenderSVG() missing top model %q:\n%s", model, light)
		}
	}
	if strings.Contains(light, "model-e") {
		t.Errorf("RenderSVG() unexpectedly shows omitted model \"model-e\" individually despite the layout's row budget:\n%s", light)
	}
	if !strings.Contains(light, "1 more") {
		t.Errorf("RenderSVG() missing an omitted-entries summary line (\"1 more\") for a limit exceeding the layout budget:\n%s", light)
	}
}

// TestRenderSVG_FlatMultiDayTrend_PointsShareMidpointWithoutOverlap covers
// buildSVGTrend's span==0 fallback: when every day's total is identical,
// maxTok-minTok is 0 and every point must fall back to the plot rectangle's
// fixed midpoint rather than dividing by a zero span or collapsing onto one
// x coordinate.
func TestRenderSVG_FlatMultiDayTrend_PointsShareMidpointWithoutOverlap(t *testing.T) {
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-18", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 500, Cost: 5.0},
		{Date: "2026-06-19", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 500, Cost: 5.0},
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 500, Cost: 5.0},
	}}
	asOf := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	light, _, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, asOf)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	const marker = `<polyline points="`
	idx := strings.Index(light, marker)
	if idx == -1 {
		t.Fatalf("RenderSVG() missing a <polyline> element for a 3-day flat trend (want the multi-point branch, not the single-point fallback):\n%s", light)
	}
	start := idx + len(marker)
	end := strings.Index(light[start:], `"`)
	if end == -1 {
		t.Fatalf("RenderSVG() polyline points attribute unterminated:\n%s", light)
	}
	points := strings.Fields(light[start : start+end])
	if len(points) != 3 {
		t.Fatalf("polyline points = %v, want exactly 3 (one per date)", points)
	}

	seenX := map[string]bool{}
	var wantY string
	for i, p := range points {
		x, y, ok := strings.Cut(p, ",")
		if !ok {
			t.Fatalf("point %q not in \"x,y\" form", p)
		}
		if seenX[x] {
			t.Errorf("point %d x=%s collides with an earlier point's x — overlapping coordinates", i, x)
		}
		seenX[x] = true
		if i == 0 {
			wantY = y
		} else if y != wantY {
			t.Errorf("point %d y=%s, want %s (every point should sit at the shared flat-trend midpoint)", i, y, wantY)
		}
		if _, err := strconv.Atoi(y); err != nil {
			t.Errorf("point %d y=%q is not a valid integer coordinate: %v", i, y, err)
		}
	}
}

// TestRenderSVG_GoldenFile locks in the exact rendered light/dark markup
// for the shared fixture, mirroring TestRender_GoldenFile's ASCII
// counterpart — written last, once the behavior tests above have settled
// the layout.
func TestRenderSVG_GoldenFile(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)
	renderedAt := time.Date(2026, 6, 22, 14, 30, 0, 0, time.UTC)

	light, dark, err := render.RenderSVG(ds, true, sum, config.BreakdownPerModel, -1, renderedAt)
	if err != nil {
		t.Fatalf("RenderSVG() error = %v", err)
	}

	wantLight, err := os.ReadFile("testdata/dashboard_card_light.golden.svg")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if light != string(wantLight) {
		t.Errorf("RenderSVG() light output does not match golden file testdata/dashboard_card_light.golden.svg\ngot:\n%s\nwant:\n%s", light, wantLight)
	}

	wantDark, err := os.ReadFile("testdata/dashboard_card_dark.golden.svg")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if dark != string(wantDark) {
		t.Errorf("RenderSVG() dark output does not match golden file testdata/dashboard_card_dark.golden.svg\ngot:\n%s\nwant:\n%s", dark, wantDark)
	}
}

// TestAltText_HappyPath_SummarizesHeadlineStats covers R8: the alt-text
// helper renders tokens, cost, and streak as one plain-text sentence for
// the <img alt> attribute a later unit builds.
func TestAltText_HappyPath_SummarizesHeadlineStats(t *testing.T) {
	ds := fixtureDataset()
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	got := render.AltText(ds, true, sum)

	for _, want := range []string{"Token Profile", "Tokens:", "4,100", "Cost:", "$6.95", "Streak:", "3 days"} {
		if !strings.Contains(got, want) {
			t.Errorf("AltText() = %q, missing %q", got, want)
		}
	}
}

// TestAltText_EmptyDataset_NamesNoDataState covers the accessibility edge
// case (Approach step 7): an empty dataset's alt text must explicitly name
// the "no data yet" state rather than a misleadingly precise all-zero
// sentence like "Tokens: 0 Cost: $0.00 Streak: 0 days".
func TestAltText_EmptyDataset_NamesNoDataState(t *testing.T) {
	ds := snapshot.MergedDataset{}
	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	sum := summary.Compute(ds, asOf, 30*24*time.Hour)

	got := render.AltText(ds, false, sum)

	if !strings.Contains(got, "No data yet") {
		t.Errorf("AltText() = %q, want it to explicitly name the \"no data yet\" state", got)
	}
	if strings.Contains(got, "Tokens: 0") {
		t.Errorf("AltText() = %q, should not report a misleadingly precise all-zero stat sentence", got)
	}
}

// TestAltText_InactiveWindowWithHistory_ShowsRealStats is AltText's
// counterpart to TestRenderSVG_InactiveWindowWithHistory_ShowsRealStatsNotNoDataYet
// (code review F3): a user with real history but nothing in the current
// window must get their real headline stats in alt text, not the brand-new
// no-data sentence.
func TestAltText_InactiveWindowWithHistory_ShowsRealStats(t *testing.T) {
	ds := snapshot.MergedDataset{}
	sum := summary.Summary{TotalTokens: 2000, TotalCost: 3.0, Streak: 0}

	got := render.AltText(ds, true, sum)

	if strings.Contains(got, "No data yet") {
		t.Errorf("AltText() = %q, shows the brand-new no-data state despite hasHistory=true", got)
	}
	if !strings.Contains(got, "2,000") || !strings.Contains(got, "$3.00") {
		t.Errorf("AltText() = %q, want it to report the real headline stats from history outside the current window", got)
	}
}
