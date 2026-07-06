// Package render composes a merged snapshot, its computed summary, and a
// breakdown mode into the single bordered ASCII dashboard card (R8): a
// title, headline summary, an asciigraph trend line, a streak indicator,
// and a usage breakdown, in that order.
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

// cardTitle is the dashboard card's heading — its first content line —
// identifying the card before any usage data.
const cardTitle = "Token Profile"

// repoURL is token-profile's own repo, credited via GeneratedByLine so
// anyone who sees the rendered card in a README can find the tool that
// produced it.
const repoURL = "https://github.com/Christophe1997/token-profile"

// titleLine renders the card's heading, appending the stat duration — the
// inclusive calendar span between ds's earliest and latest recorded day
// (e.g. "last 30 days") — so the card states what window its numbers cover.
// An empty dataset has no span to report, so the suffix is omitted.
func titleLine(ds snapshot.MergedDataset) string {
	dates, _ := dailyTokenTotals(ds.Rows)
	if len(dates) == 0 {
		return cardTitle
	}
	days := statDurationDays(dates[0], dates[len(dates)-1])
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	return fmt.Sprintf("%s — last %d %s", cardTitle, days, unit)
}

// statDurationDays returns the inclusive number of calendar days spanned by
// [first, last] (both canonical "YYYY-MM-DD" dates, per snapshot.Row.Date) —
// e.g. a single day spans 1, back-to-back days span 2.
func statDurationDays(first, last string) int {
	f, err1 := time.Parse(time.DateOnly, first)
	l, err2 := time.Parse(time.DateOnly, last)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(l.Sub(f).Hours()/24) + 1
}

// Render composes ds, sum, and mode into the dashboard card. breakdownLimit
// caps how many entries the breakdown section shows individually (the rest
// are folded into one summary line); zero or negative shows every entry.
// renderedAt is the moment this render happened, shown verbatim (absolute,
// UTC) as the "last updated" line (R9, AE3); it is an explicit input rather
// than time.Now() so callers stay deterministic and testable.
func Render(ds snapshot.MergedDataset, sum summary.Summary, mode config.BreakdownMode, breakdownLimit int, renderedAt time.Time) string {
	var lines []string
	lines = append(lines, titleLine(ds))
	lines = append(lines, "")
	lines = append(lines, summaryLine(sum))
	lines = append(lines, "")
	lines = append(lines, trendLines(ds)...)
	lines = append(lines, "")
	lines = append(lines, streakLine(sum))
	lines = append(lines, "")
	lines = append(lines, breakdownLines(ds, mode, breakdownLimit)...)
	lines = append(lines, "")
	lines = append(lines, lastUpdatedLine(renderedAt))
	return box(lines)
}

// Headline renders sum as a single confirmation line — "Tokens: X   Cost:
// $Y   Streak: N days", no box — reusing summaryLine and streakLine so
// callers that want a plain-text confirmation (rather than the full
// bordered card) don't duplicate render's own token/cost/streak formatting.
func Headline(sum summary.Summary) string {
	return summaryLine(sum) + "   " + streakLine(sum)
}

func summaryLine(sum summary.Summary) string {
	return fmt.Sprintf("Tokens: %s%s   Cost: $%.2f%s",
		formatTokens(sum.TotalTokens), changeSuffix(sum.TokenChangePct),
		sum.TotalCost, changeSuffix(sum.CostChangePct),
	)
}

// changeSuffix renders a window-over-window percentage change as
// " (+50%)" (space-prefixed, parenthesized, explicitly signed) — or ""
// when pct is nil, meaning there's no prior window to compare against.
func changeSuffix(pct *float64) string {
	if pct == nil {
		return ""
	}
	return fmt.Sprintf(" (%+.0f%%)", *pct)
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
		asciigraph.YAxisValueFormatter(func(v float64) string {
			return formatTokens(int64(v))
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

// breakdownLines renders mode's grouped entries, showing at most limit of
// them individually (sorted by descending tokens, so the top contributors
// lead) and folding whatever's left into one "N more" summary line — never
// dropping the excess silently. limit <= 0 shows every entry.
func breakdownLines(ds snapshot.MergedDataset, mode config.BreakdownMode, limit int) []string {
	lines := []string{breakdownHeading(mode)}
	entries := groupBreakdown(ds.Rows, mode)
	if len(entries) == 0 {
		return append(lines, "  "+noDataMessage)
	}

	shown, omitted := entries, []breakdownEntry(nil)
	if limit > 0 && len(entries) > limit {
		shown, omitted = entries[:limit], entries[limit:]
	}
	for _, e := range shown {
		lines = append(lines, fmt.Sprintf("  %s — %s tokens ($%.2f)", e.Label, formatTokens(e.Tokens), e.Cost))
	}
	if len(omitted) > 0 {
		var tokens int64
		var cost float64
		for _, e := range omitted {
			tokens += e.Tokens
			cost += e.Cost
		}
		lines = append(lines, fmt.Sprintf("  … %d more — %s tokens ($%.2f)", len(omitted), formatTokens(tokens), cost))
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

// GeneratedByLine renders a markdown link crediting token-profile. Unlike
// every other line in this package, it's meant to sit outside the
// bordered card — the card is injected inside a fenced code block, where
// CommonMark renders markdown syntax literally rather than as a link.
func GeneratedByLine() string {
	return "Generated by [token-profile](" + repoURL + ")"
}

// million and billion are the thresholds above which formatTokens switches
// from comma-grouped digits to a shortened "X.YM"/"X.YB" unit, since cache
// tokens (now folded into Tokens) routinely push daily totals into the tens
// or hundreds of millions, where a raw digit string is hard to scan.
const (
	million = 1_000_000
	billion = 1_000_000_000
)

// formatTokens renders n for display: thousands separators below one
// million (e.g. 12345 -> "12,345"), otherwise a shortened unit suffix (e.g.
// 12_345_678 -> "12.3M", 2_500_000_000 -> "2.5B").
func formatTokens(n int64) string {
	abs := n
	neg := n < 0
	if neg {
		abs = -abs
	}

	var s string
	switch {
	case abs >= billion:
		s = formatUnit(abs, billion, "B")
	case abs >= million:
		s = formatUnit(abs, million, "M")
	default:
		s = formatWithCommas(abs)
	}
	if neg {
		return "-" + s
	}
	return s
}

// formatUnit renders abs/divisor to one decimal place, dropping a trailing
// ".0" (e.g. 2M rather than 2.0M), followed by suffix.
func formatUnit(abs, divisor int64, suffix string) string {
	s := strconv.FormatFloat(float64(abs)/float64(divisor), 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + suffix
}

// formatWithCommas renders a non-negative n with thousands separators (e.g.
// 12345 -> "12,345").
func formatWithCommas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out strings.Builder
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteByte(s[i])
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
