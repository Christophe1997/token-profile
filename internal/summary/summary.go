// Package summary derives the headline usage summary and streak/activity
// indicator (R5, R6) from a merged snapshot dataset. It only aggregates and
// walks whatever rows it's given — the trailing window itself is already
// baked in upstream (agentsview's --since, KTD10) before the data ever
// reaches this package.
package summary

import (
	"time"

	"github.com/Christophe1997/token-profile/internal/snapshot"
)

// Summary is the headline usage summary derived from a MergedDataset: total
// tokens and estimated cost across every row (R5), plus the current
// consecutive-day activity streak as of AsOf (R6). It carries only the
// derived numbers, not layout — card composition is U5's job.
type Summary struct {
	TotalTokens int64
	TotalCost   float64
	Streak      int
	AsOf        time.Time
}

// Compute derives Summary from ds. asOf stands in for "today" (UTC) when
// walking the streak backward; production call sites pass
// time.Now().UTC() while tests pass a fixed time so streak results stay
// deterministic.
func Compute(ds snapshot.MergedDataset, asOf time.Time) Summary {
	var totalTokens int64
	var totalCost float64
	active := make(map[time.Time]bool, len(ds.Rows))
	for _, r := range ds.Rows {
		totalTokens += r.Tokens
		totalCost += r.Cost
		if r.Tokens == 0 && r.Cost == 0 {
			continue // zero-usage rows don't mark a day active (matches Resolve's own omission convention)
		}
		if d, err := time.Parse(time.DateOnly, r.Date); err == nil {
			active[d] = true
		}
	}

	return Summary{
		TotalTokens: totalTokens,
		TotalCost:   totalCost,
		Streak:      streak(active, civilDate(asOf)),
		AsOf:        asOf,
	}
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
