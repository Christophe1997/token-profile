// Package summary derives the headline usage summary and streak/activity
// indicator (R5, R6) from a merged snapshot dataset. Since Write now
// accumulates a machine's full history rather than rolling off with the
// trailing window (see internal/snapshot), ds can span far more than one
// reporting window — Compute itself scopes totals to window and compares
// against the immediately preceding, equal-length window, while streak
// walks ds's full history unbounded by window.
package summary

import (
	"time"

	"github.com/Christophe1997/token-profile/internal/snapshot"
)

// Summary is the headline usage summary derived from a MergedDataset: total
// tokens and estimated cost across the current window (R5), the current
// consecutive-day activity streak as of AsOf (R6), and each total's
// percentage change against the immediately preceding, equal-length window
// (nil when that prior window has no data to compare against). It carries
// only the derived numbers, not layout — card composition is U5's job.
type Summary struct {
	TotalTokens    int64
	TotalCost      float64
	Streak         int
	AsOf           time.Time
	TokenChangePct *float64
	CostChangePct  *float64
}

// Compute derives Summary from ds. asOf stands in for "today" (UTC) when
// walking the streak backward and scoping window; production call sites
// pass time.Now().UTC() while tests pass a fixed time so results stay
// deterministic. window bounds TotalTokens/TotalCost to [asOf-window, asOf]
// and defines the immediately preceding window compared against for
// TokenChangePct/CostChangePct; it does not bound the streak, which reports
// however many consecutive active days ds's full history actually has.
func Compute(ds snapshot.MergedDataset, asOf time.Time, window time.Duration) Summary {
	currentSince := civilDate(asOf.Add(-window))
	previousSince := civilDate(asOf.Add(-2 * window))

	var currentTokens, previousTokens int64
	var currentCost, previousCost float64
	active := make(map[time.Time]bool, len(ds.Rows))
	for _, r := range ds.Rows {
		d, err := time.Parse(time.DateOnly, r.Date)
		if err != nil {
			continue
		}
		if !(r.Tokens == 0 && r.Cost == 0) {
			active[d] = true // zero-usage rows don't mark a day active (matches Resolve's own omission convention)
		}
		switch {
		case !d.Before(currentSince):
			currentTokens += r.Tokens
			currentCost += r.Cost
		case !d.Before(previousSince):
			previousTokens += r.Tokens
			previousCost += r.Cost
		}
	}

	return Summary{
		TotalTokens:    currentTokens,
		TotalCost:      currentCost,
		Streak:         streak(active, civilDate(asOf)),
		AsOf:           asOf,
		TokenChangePct: changePct(previousTokens, currentTokens),
		CostChangePct:  changePct(previousCost, currentCost),
	}
}

// changePct returns the percentage change from previous to current, or nil
// when previous is zero — an empty previous window makes "percentage
// change" undefined (and would otherwise divide by zero), so the rate is
// omitted rather than reported as a misleading "+100%" or "+Inf%".
func changePct[T int64 | float64](previous, current T) *float64 {
	if previous == 0 {
		return nil
	}
	pct := (float64(current) - float64(previous)) / float64(previous) * 100
	return &pct
}

// civilDate truncates t to UTC midnight of its calendar date, matching the
// zone-less keys time.Parse(time.DateOnly, ...) produces for Row.Date, so a
// caller-supplied asOf carrying a real time-of-day (e.g. time.Now().UTC())
// still lines up with the calendar dates recorded in ds.
func civilDate(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// streak walks backward day by day from today, counting consecutive active
// days until it hits one with no recorded usage. If today itself is
// inactive, it grants a one-day grace — today may simply not have synced
// yet — and starts counting from yesterday instead, rather than reporting a
// streak of 0 solely because of today's absence.
func streak(active map[time.Time]bool, today time.Time) int {
	cursor := today
	if !active[cursor] {
		cursor = cursor.AddDate(0, 0, -1)
	}
	var n int
	for active[cursor] {
		n++
		cursor = cursor.AddDate(0, 0, -1)
	}
	return n
}
