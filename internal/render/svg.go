package render

import (
	"bytes"
	"fmt"
	"html"
	"slices"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// Fixed layout coordinates for the SVG card (KTD2, KTD3): the canvas is a
// constant size regardless of dataset or palette — unlike box()'s
// auto-sizing ASCII border, there's no "grow to fit" here, so every
// content-length risk (an unbounded breakdown, an outlier label) is
// resolved by truncation rather than a bigger canvas (see
// effectiveBreakdownLimit and truncateLabel).
const (
	svgWidth  = 640
	svgHeight = 520

	svgMarginX = 32

	svgStatBlockGap = 300

	svgPlotTop    = 168
	svgPlotBottom = 288
	svgPlotLeft   = 90
	svgPlotRight  = 608

	svgBreakdownFirstRowY  = svgPlotBottom + 96
	svgBreakdownRowHeight  = 22
	svgBreakdownRowBudget  = 4 // shown rows; a 5th slot is reserved for the omitted-count summary line
	svgBreakdownLabelRunes = 22
)

// svgPalette is the small set of colors that differ between the light and
// dark card variants (KTD2) — every other layout detail is one shared
// template.
type svgPalette struct {
	Background string
	Border     string
	Title      string
	Text       string
	Muted      string
	Positive   string
	Negative   string
	Accent     string
	Grid       string
}

var svgLightPalette = svgPalette{
	Background: "#ffffff",
	Border:     "#d0d7de",
	Title:      "#0969da",
	Text:       "#1f2328",
	Muted:      "#656d76",
	Positive:   "#1a7f37",
	Negative:   "#cf222e",
	Accent:     "#0969da",
	Grid:       "#d8dee4",
}

var svgDarkPalette = svgPalette{
	Background: "#0d1117",
	Border:     "#30363d",
	Title:      "#58a6ff",
	Text:       "#c9d1d9",
	Muted:      "#8b949e",
	Positive:   "#3fb950",
	Negative:   "#f85149",
	Accent:     "#58a6ff",
	Grid:       "#21262d",
}

// svgStat is one headline stat block (tokens or cost), positioned at a
// fixed X offset so multiple stats lay out side by side.
type svgStat struct {
	X          int
	Label      string
	Value      string
	Delta      string
	DeltaClass string // "positive", "negative", or "" (no delta, or unchanged)
}

// svgBreakdownRow is one shown breakdown entry, pre-positioned at its row's
// fixed Y so the template needs no per-row arithmetic.
type svgBreakdownRow struct {
	Y      int
	Label  string
	Tokens string
	Cost   string
}

// svgTextLine is a single positioned line of text — used for the
// omitted-entries breakdown summary, whose Y depends on how many rows were
// actually shown.
type svgTextLine struct {
	Y    int
	Text string
}

// svgTrend is the trend chart's data, pre-scaled to the fixed plot
// rectangle (svgPlotTop/Bottom/Left/Right). Single covers the degenerate
// one-data-point case, mirroring trendLines' own single-day branch: a
// meaningful point/label instead of a one-point polyline.
type svgTrend struct {
	Single     bool
	PointText  string
	Polyline   string
	StartLabel string
	EndLabel   string
	MaxLabel   string
	MinLabel   string
}

// svgCardData is the template's root data. One value serves both palette
// variants — RenderSVG swaps Palette and re-executes the same template
// (KTD2) rather than building two separate data sets.
type svgCardData struct {
	Palette svgPalette

	Width, Height int
	Title         string

	NoData        bool
	NoDataMessage string

	Stats            []svgStat
	Streak           string
	BreakdownHeading string
	BreakdownRows    []svgBreakdownRow
	OmittedLine      *svgTextLine
	Trend            svgTrend
	LastUpdated      string
}

// xmlEscape escapes s for safe embedding as SVG text content. text/template
// (KTD1) does none of html/template's auto-escaping, so any value that
// ultimately came from external data (a model or agent name) must be
// escaped by hand before it reaches the template.
func xmlEscape(s string) string {
	return html.EscapeString(s)
}

// svgTemplateSource is the shared card layout (KTD1, KTD2): one
// text/template drawing fixed-position rects/text/polylines, with only
// Palette colors and the content fields (Stats, Trend, BreakdownRows, ...)
// varying between calls. Coordinates are hardcoded to match the constants
// above exactly, since they never vary independently of them — see
// buildSVGCardData and buildSVGTrend for the Go-side math that must stay in
// sync with these numbers.
const svgTemplateSource = `<svg xmlns="http://www.w3.org/2000/svg" width="{{.Width}}" height="{{.Height}}" viewBox="0 0 {{.Width}} {{.Height}}" role="img" font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif">
<title>{{esc .Title}}</title>
<rect width="100%" height="100%" fill="{{.Palette.Background}}"/>
<rect x="1" y="1" width="638" height="518" rx="12" fill="none" stroke="{{.Palette.Border}}" stroke-width="2"/>
<text x="32" y="40" font-size="22" font-weight="600" fill="{{.Palette.Title}}">{{esc .Title}}</text>
<line x1="32" y1="56" x2="608" y2="56" stroke="{{.Palette.Border}}" stroke-width="1"/>
{{if .NoData}}
<text x="320" y="260" font-size="16" fill="{{.Palette.Muted}}" text-anchor="middle">{{esc .NoDataMessage}}</text>
{{else}}
{{range .Stats}}
<text x="{{.X}}" y="84" font-size="12" font-weight="600" letter-spacing="1" fill="{{$.Palette.Muted}}">{{esc .Label}}</text>
<text x="{{.X}}" y="118" font-size="26" font-weight="700" fill="{{$.Palette.Text}}">{{esc .Value}}{{if .Delta}}<tspan font-size="15" font-weight="600" fill="{{deltaColor $.Palette .DeltaClass}}"> {{esc .Delta}}</tspan>{{end}}</text>
{{end}}
<text x="32" y="152" font-size="12" font-weight="600" letter-spacing="1" fill="{{.Palette.Muted}}">TREND</text>
<line x1="90" y1="288" x2="608" y2="288" stroke="{{.Palette.Grid}}" stroke-width="1"/>
{{if .Trend.Single}}
<circle cx="349" cy="228" r="5" fill="{{.Palette.Accent}}"/>
<text x="349" y="208" font-size="13" fill="{{.Palette.Text}}" text-anchor="middle">{{esc .Trend.PointText}}</text>
{{else}}
<polyline points="{{.Trend.Polyline}}" fill="none" stroke="{{.Palette.Accent}}" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>
<text x="90" y="306" font-size="12" fill="{{.Palette.Muted}}">{{esc .Trend.StartLabel}}</text>
<text x="608" y="306" font-size="12" fill="{{.Palette.Muted}}" text-anchor="end">{{esc .Trend.EndLabel}}</text>
<text x="82" y="172" font-size="12" fill="{{.Palette.Muted}}" text-anchor="end">{{esc .Trend.MaxLabel}}</text>
<text x="82" y="288" font-size="12" fill="{{.Palette.Muted}}" text-anchor="end">{{esc .Trend.MinLabel}}</text>
{{end}}
<text x="32" y="328" font-size="16" font-weight="600" fill="{{.Palette.Text}}">{{esc .Streak}}</text>
<text x="32" y="360" font-size="12" font-weight="600" letter-spacing="1" fill="{{.Palette.Muted}}">{{esc .BreakdownHeading}}</text>
{{range .BreakdownRows}}
<text x="32" y="{{.Y}}" font-size="13" fill="{{$.Palette.Text}}">{{esc .Label}}</text>
<text x="420" y="{{.Y}}" font-size="13" fill="{{$.Palette.Muted}}" text-anchor="end">{{esc .Tokens}}</text>
<text x="608" y="{{.Y}}" font-size="13" fill="{{$.Palette.Muted}}" text-anchor="end">{{esc .Cost}}</text>
{{end}}
{{with .OmittedLine}}
<text x="32" y="{{.Y}}" font-size="13" fill="{{$.Palette.Muted}}">{{esc .Text}}</text>
{{end}}
{{end}}
<text x="32" y="504" font-size="11" fill="{{.Palette.Muted}}">{{esc .LastUpdated}}</text>
</svg>
`

var svgTemplate = template.Must(template.New("dashboard-card").Funcs(template.FuncMap{
	"esc": xmlEscape,
	"deltaColor": func(p svgPalette, class string) string {
		switch class {
		case "positive":
			return p.Positive
		case "negative":
			return p.Negative
		default:
			return p.Muted
		}
	},
}).Parse(svgTemplateSource))

// RenderSVG composes ds, sum, and mode into light and dark SVG variants of
// the dashboard card (R1, R4): the same title/stat-duration, headline
// stats with deltas, trend chart, streak, and breakdown truncation Render
// already shows (R3) — reusing this package's own grouping/formatting
// helpers directly (KTD4) — laid out on one fixed-size canvas per theme
// rather than Render's auto-sizing box (see the package-level layout
// constants).
func RenderSVG(ds snapshot.MergedDataset, sum summary.Summary, mode config.BreakdownMode, breakdownLimit int, renderedAt time.Time) (light, dark string, err error) {
	data := buildSVGCardData(ds, sum, mode, breakdownLimit, renderedAt)

	data.Palette = svgLightPalette
	var lightBuf bytes.Buffer
	if err := svgTemplate.Execute(&lightBuf, data); err != nil {
		return "", "", fmt.Errorf("rendering light SVG card: %w", err)
	}

	data.Palette = svgDarkPalette
	var darkBuf bytes.Buffer
	if err := svgTemplate.Execute(&darkBuf, data); err != nil {
		return "", "", fmt.Errorf("rendering dark SVG card: %w", err)
	}

	return lightBuf.String(), darkBuf.String(), nil
}

// AltText renders ds/sum's headline stats as one plain-text sentence (R8),
// consumed by a later unit's <img alt> attribute. An empty dataset
// explicitly names the "no data yet" state (reusing noDataMessage) rather
// than a misleadingly precise all-zero sentence like "Tokens: 0 Cost:
// $0.00 Streak: 0 days".
func AltText(ds snapshot.MergedDataset, sum summary.Summary) string {
	if len(ds.Rows) == 0 {
		return CardTitle + " — " + noDataMessage
	}
	return fmt.Sprintf("%s. %s. %s.", titleLine(ds), summaryLine(sum), streakLine(sum))
}

func buildSVGCardData(ds snapshot.MergedDataset, sum summary.Summary, mode config.BreakdownMode, breakdownLimit int, renderedAt time.Time) svgCardData {
	data := svgCardData{
		Width:       svgWidth,
		Height:      svgHeight,
		Title:       titleLine(ds),
		LastUpdated: lastUpdatedLine(renderedAt),
	}

	if len(ds.Rows) == 0 {
		data.NoData = true
		data.NoDataMessage = noDataMessage
		return data
	}

	data.Stats = []svgStat{
		{
			X: svgMarginX, Label: "TOKENS",
			Value: formatTokens(sum.TotalTokens),
			Delta: deltaText(sum.TokenChangePct), DeltaClass: deltaClass(sum.TokenChangePct),
		},
		{
			X: svgMarginX + svgStatBlockGap, Label: "COST",
			Value: fmt.Sprintf("$%.2f", sum.TotalCost),
			Delta: deltaText(sum.CostChangePct), DeltaClass: deltaClass(sum.CostChangePct),
		},
	}
	data.Streak = streakLine(sum)
	data.BreakdownHeading = breakdownHeading(mode)
	data.BreakdownRows, data.OmittedLine = buildSVGBreakdown(ds, mode, breakdownLimit)
	data.Trend = buildSVGTrend(ds)
	return data
}

func deltaText(pct *float64) string {
	if pct == nil {
		return ""
	}
	return fmt.Sprintf("%+.0f%%", *pct)
}

func deltaClass(pct *float64) string {
	switch {
	case pct == nil:
		return ""
	case *pct > 0:
		return "positive"
	case *pct < 0:
		return "negative"
	default:
		return ""
	}
}

// effectiveBreakdownLimit bounds limit to the SVG layout's fixed row
// budget. Unlike Render's ASCII box (which grows to fit any number of
// entries), the SVG canvas can't grow, so a caller-supplied "unlimited"
// (<=0) or oversized limit is clamped to the layout's own capacity rather
// than trusted verbatim — the fixed canvas always wins.
func effectiveBreakdownLimit(limit int) int {
	if limit <= 0 || limit > svgBreakdownRowBudget {
		return svgBreakdownRowBudget
	}
	return limit
}

// buildSVGBreakdown groups ds per mode (reusing groupBreakdown directly,
// KTD4) and splits it into up to effectiveBreakdownLimit shown rows plus
// one folded omitted-entries summary line, mirroring breakdownLines' own
// shown/omitted split but producing per-column fields (Label/Tokens/Cost)
// instead of one preformatted string, since the SVG lays them out in
// separate columns rather than one line of text.
func buildSVGBreakdown(ds snapshot.MergedDataset, mode config.BreakdownMode, limit int) (rows []svgBreakdownRow, omitted *svgTextLine) {
	entries := groupBreakdown(ds.Rows, mode)
	limit = effectiveBreakdownLimit(limit)

	shown, rest := entries, []breakdownEntry(nil)
	if len(entries) > limit {
		shown, rest = entries[:limit], entries[limit:]
	}

	for i, e := range shown {
		rows = append(rows, svgBreakdownRow{
			Y:      svgBreakdownFirstRowY + i*svgBreakdownRowHeight,
			Label:  truncateLabel(e.Label, svgBreakdownLabelRunes),
			Tokens: formatTokens(e.Tokens),
			Cost:   fmt.Sprintf("$%.2f", e.Cost),
		})
	}

	if len(rest) == 0 {
		return rows, nil
	}
	var tokens int64
	var cost float64
	for _, e := range rest {
		tokens += e.Tokens
		cost += e.Cost
	}
	return rows, &svgTextLine{
		Y:    svgBreakdownFirstRowY + len(shown)*svgBreakdownRowHeight,
		Text: fmt.Sprintf("… %d more — %s tokens ($%.2f)", len(rest), formatTokens(tokens), cost),
	}
}

// truncateLabel shortens s to at most maxRunes runes with a trailing
// ellipsis. Unlike the ASCII card's box() (which widens to fit its widest
// line), the SVG's breakdown column has a fixed width, so an outlier
// model/agent name is truncated rather than allowed to overflow it.
func truncateLabel(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-1]) + "…"
}

// buildSVGTrend scales ds's daily token totals (reusing dailyTokenTotals
// directly, KTD4) into the fixed plot rectangle
// (svgPlotTop/Bottom/Left/Right). A single distinct date can't define a
// scale or a line, so — mirroring trendLines' own single-day branch — it
// renders as one labeled point instead of a degenerate one-point polyline.
func buildSVGTrend(ds snapshot.MergedDataset) svgTrend {
	dates, tokens := dailyTokenTotals(ds.Rows)
	if len(dates) == 1 {
		return svgTrend{
			Single:    true,
			PointText: fmt.Sprintf("%s: %s tokens", shortDate(dates[0]), formatTokens(int64(tokens[0]))),
		}
	}

	minTok, maxTok := slices.Min(tokens), slices.Max(tokens)
	span := maxTok - minTok

	var points strings.Builder
	xStep := float64(svgPlotRight-svgPlotLeft) / float64(len(dates)-1)
	for i, v := range tokens {
		x := svgPlotLeft + int(float64(i)*xStep)
		y := (svgPlotTop + svgPlotBottom) / 2
		if span > 0 {
			y = svgPlotBottom - int((v-minTok)/span*float64(svgPlotBottom-svgPlotTop))
		}
		if i > 0 {
			points.WriteByte(' ')
		}
		fmt.Fprintf(&points, "%d,%d", x, y)
	}

	return svgTrend{
		Polyline:   points.String(),
		StartLabel: shortDate(dates[0]),
		EndLabel:   shortDate(dates[len(dates)-1]),
		MaxLabel:   formatTokens(int64(maxTok)),
		MinLabel:   formatTokens(int64(minTok)),
	}
}
