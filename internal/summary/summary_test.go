package summary_test

import (
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// TestCompute_EmptyDataset covers a truly empty MergedDataset (zero rows,
// e.g. before any machine has ever run): Compute must report zero totals and
// a zero streak rather than erroring.
func TestCompute_EmptyDataset(t *testing.T) {
	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	got := summary.Compute(snapshot.MergedDataset{}, asOf, 30*24*time.Hour)

	want := summary.Summary{TotalTokens: 0, TotalCost: 0, Streak: 0, AsOf: asOf}
	if got != want {
		t.Errorf("Compute() = %+v, want %+v", got, want)
	}
}

// TestCompute_SingleDayFirstRun covers the first-ever run: a single day of
// data must produce correct totals and a streak of 1, without erroring on
// otherwise-empty history.
func TestCompute_SingleDayFirstRun(t *testing.T) {
	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
	}}

	got := summary.Compute(ds, asOf, 30*24*time.Hour)

	want := summary.Summary{TotalTokens: 100, TotalCost: 1.0, Streak: 1, AsOf: asOf}
	if got != want {
		t.Errorf("Compute() = %+v, want %+v", got, want)
	}
}

// TestCompute_TodayHasNoDataYet covers a fresh run before agentsview has
// synced today's session: today itself has no row, but the streak must
// still count backward starting from yesterday rather than immediately
// reporting 0 just because today is empty so far. asOf carries a realistic
// mid-afternoon time (not midnight) to also confirm Compute resolves it to
// its UTC calendar date before comparing against Row dates.
func TestCompute_TodayHasNoDataYet(t *testing.T) {
	asOf := time.Date(2026, 6, 4, 15, 30, 0, 0, time.UTC)
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 10, Cost: 1.0},
		{Date: "2026-06-02", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 10, Cost: 1.0},
		{Date: "2026-06-03", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 10, Cost: 1.0},
	}}

	got := summary.Compute(ds, asOf, 30*24*time.Hour)

	if got.Streak != 3 {
		t.Errorf("Compute().Streak = %d, want 3 (today's absence must not break the streak)", got.Streak)
	}
	if got.TotalTokens != 30 || got.TotalCost != 3.0 {
		t.Errorf("Compute() totals = (%d, %v), want (30, 3.0)", got.TotalTokens, got.TotalCost)
	}
}

// TestCompute_TwelveConsecutiveDaysThenGap covers the headline happy path:
// 12 consecutive active days immediately followed by a gap (no row for that
// date or any later date) yields a streak of 12 when asOf is the day right
// after the gap. One date (2026-05-25) carries a second row from a
// different agent/model, confirming totals sum across every row regardless
// of streak and that a date with multiple rows still counts as one active
// day.
func TestCompute_TwelveConsecutiveDaysThenGap(t *testing.T) {
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	var rows []snapshot.Row
	for i := range 12 {
		date := start.AddDate(0, 0, i).Format(time.DateOnly)
		rows = append(rows, snapshot.Row{Date: date, Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0})
	}
	rows = append(rows, snapshot.Row{Date: "2026-05-25", Agent: "codex", Model: "gpt-5.4", Tokens: 50, Cost: 2.0})
	ds := snapshot.MergedDataset{Rows: rows}

	asOf := start.AddDate(0, 0, 12) // 2026-06-01: the day right after the last active day (the gap)

	got := summary.Compute(ds, asOf, 30*24*time.Hour)

	if got.Streak != 12 {
		t.Errorf("Compute().Streak = %d, want 12", got.Streak)
	}
	if got.TotalTokens != 1250 || got.TotalCost != 14.0 {
		t.Errorf("Compute() totals = (%d, %v), want (1250, 14.0)", got.TotalTokens, got.TotalCost)
	}
}

// TestCompute_ZeroUsageRowNotActive covers the "zero usage isn't a real
// row" convention (matching U2/U3): a row present for a date but carrying
// zero tokens and zero cost must not count that date as active for streak
// purposes, even though such rows shouldn't normally reach merge (Resolve
// already omits them) — Compute stays defensive regardless.
func TestCompute_ZeroUsageRowNotActive(t *testing.T) {
	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 0, Cost: 0},
	}}

	got := summary.Compute(ds, asOf, 30*24*time.Hour)

	if got.Streak != 0 {
		t.Errorf("Compute().Streak = %d, want 0 (a zero-usage row must not count as an active day)", got.Streak)
	}
}

// TestCompute_TotalsScopedToCurrentWindowOnly covers window-scoping: since
// Write now accumulates a machine's full history (rather than rolling off
// with the trailing window), Compute must itself exclude rows older than
// window from TotalTokens/TotalCost — a caller can hand it years of
// accumulated history and still get "the current window"'s totals back.
func TestCompute_TotalsScopedToCurrentWindowOnly(t *testing.T) {
	asOf := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-05-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 10.0}, // outside a 7-day window
		{Date: "2026-06-19", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0},
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 50, Cost: 0.5},
	}}

	got := summary.Compute(ds, asOf, 7*24*time.Hour)

	if got.TotalTokens != 150 || got.TotalCost != 1.5 {
		t.Errorf("Compute() totals = (%d, %v), want (150, 1.5) (2026-05-01's row is outside the 7-day window)", got.TotalTokens, got.TotalCost)
	}
}

// TestCompute_Streak_UsesFullHistoryBeyondWindow covers the accumulated-
// history upside for streak: unlike totals (window-scoped), a streak run
// longer than window must still be reported in full, since Write no longer
// rolls old days off this machine's snapshot (see internal/snapshot).
func TestCompute_Streak_UsesFullHistoryBeyondWindow(t *testing.T) {
	start := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	var rows []snapshot.Row
	for i := range 12 {
		date := start.AddDate(0, 0, i).Format(time.DateOnly)
		rows = append(rows, snapshot.Row{Date: date, Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 1.0})
	}
	ds := snapshot.MergedDataset{Rows: rows}
	asOf := start.AddDate(0, 0, 11) // the 12th (and latest) consecutive active day

	got := summary.Compute(ds, asOf, 7*24*time.Hour) // window (7d) is shorter than the 12-day streak

	if got.Streak != 12 {
		t.Errorf("Compute().Streak = %d, want 12 (streak must not be capped by the shorter totals window)", got.Streak)
	}
}

// TestCompute_ChangePct_ComputesVsPreviousWindow covers the window-over-
// window rate: given a current window and an immediately preceding,
// equal-length previous window, both TokenChangePct and CostChangePct must
// reflect the percentage change between them.
func TestCompute_ChangePct_ComputesVsPreviousWindow(t *testing.T) {
	window := 7 * 24 * time.Hour
	asOf := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		// previous window: 2026-06-06 through 2026-06-12 (inclusive)
		{Date: "2026-06-10", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 100, Cost: 10.0},
		// current window: 2026-06-13 through 2026-06-20 (inclusive of asOf)
		{Date: "2026-06-19", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 150, Cost: 5.0},
	}}

	got := summary.Compute(ds, asOf, window)

	if got.TokenChangePct == nil || *got.TokenChangePct != 50 {
		t.Errorf("Compute().TokenChangePct = %v, want 50 (150 vs 100 previous = +50%%)", got.TokenChangePct)
	}
	if got.CostChangePct == nil || *got.CostChangePct != -50 {
		t.Errorf("Compute().CostChangePct = %v, want -50 (5.0 vs 10.0 previous = -50%%)", got.CostChangePct)
	}
}

// TestCompute_ChangePct_NilWhenNoPreviousWindowData covers a new adopter (or
// one who has run token-profile for less than 2x window): with nothing in
// the previous window to compare against, the rate is undefined rather than
// a misleading "+100%" or divide-by-zero result.
func TestCompute_ChangePct_NilWhenNoPreviousWindowData(t *testing.T) {
	window := 7 * 24 * time.Hour
	asOf := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-19", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 150, Cost: 5.0},
	}}

	got := summary.Compute(ds, asOf, window)

	if got.TokenChangePct != nil {
		t.Errorf("Compute().TokenChangePct = %v, want nil (no previous-window data to compare against)", *got.TokenChangePct)
	}
	if got.CostChangePct != nil {
		t.Errorf("Compute().CostChangePct = %v, want nil (no previous-window data to compare against)", *got.CostChangePct)
	}
}
