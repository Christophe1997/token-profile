// Package render composes a merged snapshot, its computed summary, and a
// breakdown mode into the single bordered ASCII dashboard card (R8): a
// headline summary, an asciigraph trend line, a streak indicator, and a
// usage breakdown, in that order.
package render

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guptarohit/asciigraph"

	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// noDataMessage is shown in place of the trend graph and breakdown when ds
// has no rows yet (e.g. a brand-new adopter's first run), since asciigraph
// panics on an empty series rather than rendering something meaningful.
const noDataMessage = "No data yet — run token-profile to record your first day."

// Render composes ds, sum, and mode into the dashboard card. renderedAt is
// the moment this render happened, shown verbatim (absolute, UTC) as the
// "last updated" line (R9, AE3); it is an explicit input rather than
// time.Now() so callers stay deterministic and testable.
func Render(ds snapshot.MergedDataset, sum summary.Summary, mode config.BreakdownMode, renderedAt time.Time) string {
	var lines []string
	lines = append(lines, summaryLine(sum))
	lines = append(lines, "")
	lines = append(lines, trendLines(ds)...)
	lines = append(lines, "")
	lines = append(lines, streakLine(sum))
	lines = append(lines, "")
	lines = append(lines, breakdownLines(ds, mode)...)
	lines = append(lines, "")
	lines = append(lines, lastUpdatedLine(renderedAt))
	return box(lines)
}

func summaryLine(sum summary.Summary) string {
	return fmt.Sprintf("Tokens: %s   Cost: $%.2f", formatTokens(sum.TotalTokens), sum.TotalCost)
}

func trendLines(ds snapshot.MergedDataset) []string {
	if len(ds.Rows) == 0 {
		return []string{"Trend:", noDataMessage}
	}
	dates, tokens := dailyTokenTotals(ds.Rows)
	if len(dates) == 1 {
		// asciigraph's tick placement is undefined on a single point: with
		// only one x-axis position to place them at, a tick count forced up
		// to its minimum of 2 (XAxisRange(0, 0)) prints the same date label
		// twice rather than once. A single explicit line is both correct
		// and simpler than coaxing asciigraph through a degenerate plot.
		return []string{"Trend:", fmt.Sprintf("  %s: %s tokens", shortDate(dates[0]), formatTokens(int64(tokens[0])))}
	}
	// A narrow default width crowds ticks together, so asciigraph rounds
	// several distinct x-axis positions to the same day and prints
	// duplicate/missing date labels. Widening the plot in proportion to the
	// number of days (floored at 40) gives XAxisTickCount enough room to
	// resolve each tick to a distinct index.
	width := max(len(dates)*4, 40)
	tickCount := max(2, min(len(dates), 6)) // asciigraph requires tick count >= 2
	graph := asciigraph.Plot(tokens,
		asciigraph.Height(6),
		asciigraph.Width(width),
		asciigraph.Caption("tokens/day"),
		asciigraph.XAxisRange(0, float64(len(dates)-1)),
		asciigraph.XAxisTickCount(tickCount),
		asciigraph.XAxisValueFormatter(func(v float64) string {
			i := int(v + 0.5)
			if i < 0 || i >= len(dates) {
				return ""
			}
			return shortDate(dates[i])
		}),
	)
	return append([]string{"Trend:"}, strings.Split(graph, "\n")...)
}

// dailyTokenTotals groups rows by date, summing tokens across every
// agent/model for that date, and returns dates sorted chronologically
// alongside each date's token total — the daily series asciigraph plots.
// Row.Date is already the canonical "YYYY-MM-DD" form (see the snapshot
// package), so lexical sort order is chronological order.
func dailyTokenTotals(rows []snapshot.Row) (dates []string, tokens []float64) {
	totals := make(map[string]int64)
	for _, r := range rows {
		totals[r.Date] += r.Tokens
	}
	dates = slices.Sorted(maps.Keys(totals))
	tokens = make([]float64, len(dates))
	for i, d := range dates {
		tokens[i] = float64(totals[d])
	}
	return dates, tokens
}

// shortDate renders a canonical "YYYY-MM-DD" date as a compact "MM-DD" for
// the trend graph's x-axis ticks. Unlike the "last updated" line (AE3),
// this axis isn't the freshness signal, so the shorter form is fine.
func shortDate(date string) string {
	if len(date) != len(time.DateOnly) {
		return date
	}
	return date[5:]
}

func streakLine(sum summary.Summary) string {
	unit := "days"
	if sum.Streak == 1 {
		unit = "day"
	}
	return fmt.Sprintf("Streak: %d %s", sum.Streak, unit)
}

// breakdownEntry is one grouped row (by model, agent, or the whole dataset)
// in the rendered breakdown block.
type breakdownEntry struct {
	Label  string
	Tokens int64
	Cost   float64
}

func breakdownLines(ds snapshot.MergedDataset, mode config.BreakdownMode) []string {
	lines := []string{breakdownHeading(mode)}
	entries := groupBreakdown(ds.Rows, mode)
	if len(entries) == 0 {
		return append(lines, "  "+noDataMessage)
	}
	for _, e := range entries {
		lines = append(lines, fmt.Sprintf("  %s — %s tokens ($%.2f)", e.Label, formatTokens(e.Tokens), e.Cost))
	}
	return lines
}

func breakdownHeading(mode config.BreakdownMode) string {
	switch mode {
	case config.BreakdownPerTool:
		return "Breakdown (per tool):"
	case config.BreakdownCombined:
		return "Breakdown (combined):"
	default:
		return "Breakdown (per model):"
	}
}

// groupBreakdown groups rows per mode: by Model (BreakdownPerModel, the
// default), by Agent (BreakdownPerTool), or into a single combined total
// (BreakdownCombined). It operates on snapshot.Row directly rather than
// reusing agentsview.Dataset's ByModel/ByTool, since that's a different Row
// type from a different package. Entries are sorted by descending tokens,
// ties broken by label, so the most significant contributor leads.
func groupBreakdown(rows []snapshot.Row, mode config.BreakdownMode) []breakdownEntry {
	if len(rows) == 0 {
		return nil
	}
	if mode == config.BreakdownCombined {
		var e breakdownEntry
		e.Label = "All usage"
		for _, r := range rows {
			e.Tokens += r.Tokens
			e.Cost += r.Cost
		}
		return []breakdownEntry{e}
	}

	key := func(r snapshot.Row) string { return r.Model }
	if mode == config.BreakdownPerTool {
		key = func(r snapshot.Row) string { return r.Agent }
	}

	totals := make(map[string]breakdownEntry)
	for _, r := range rows {
		k := key(r)
		e := totals[k]
		e.Label = k
		e.Tokens += r.Tokens
		e.Cost += r.Cost
		totals[k] = e
	}
	entries := slices.Collect(maps.Values(totals))
	slices.SortFunc(entries, func(a, b breakdownEntry) int {
		return cmp.Or(
			cmp.Compare(b.Tokens, a.Tokens),
			cmp.Compare(a.Label, b.Label),
		)
	})
	return entries
}

func lastUpdatedLine(renderedAt time.Time) string {
	return "Last updated: " + renderedAt.UTC().Format("2006-01-02 15:04 MST")
}

// formatTokens renders n with thousands separators for readability (e.g.
// 12345 -> "12,345").
func formatTokens(n int64) string {
	s := strconv.FormatInt(n, 10)
	s, neg := strings.CutPrefix(s, "-")
	var out strings.Builder
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteByte(s[i])
	}
	if neg {
		return "-" + out.String()
	}
	return out.String()
}

// box wraps lines in a single bordered ASCII box, padding every line to the
// width of the widest one so the border stays consistent across the whole
// render (asciigraph's own plot lines are not equal-width — trailing
// whitespace is trimmed per row — so this padding is required, not
// cosmetic).
func box(lines []string) string {
	width := 0
	for _, l := range lines {
		if w := utf8.RuneCountInString(l); w > width {
			width = w
		}
	}

	var b strings.Builder
	b.WriteString("┌" + strings.Repeat("─", width+2) + "┐\n")
	for _, l := range lines {
		pad := width - utf8.RuneCountInString(l)
		b.WriteString("│ " + l + strings.Repeat(" ", pad) + " │\n")
	}
	b.WriteString("└" + strings.Repeat("─", width+2) + "┘")
	return b.String()
}
