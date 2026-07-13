package render

import (
	"bytes"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	svg "github.com/ajstarks/svgo"

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
//
// The layout is the "stat-tile dashboard" direction locked in during
// brainstorming: three stat tiles (tokens/cost/streak) with colored delta
// badges, a gradient-filled area under the trend line, and bar-style
// breakdown rows.
const (
	svgWidth  = 640
	svgHeight = 520

	svgMarginX = 32

	svgTileTop     = 68
	svgTileWidth   = 180
	svgTileHeight  = 76
	svgTileGap     = 18
	svgTileLabelDY = 20 // label baseline, offset from the tile's top edge
	svgTileValueDY = 46 // value baseline, offset from the tile's top edge
	svgTileBadgeDY = 54 // badge rect top, offset from the tile's top edge
	svgTileBadgeH  = 20
	svgTileBadgeRX = 10
	svgTilePadX    = 12

	svgTrendHeadingY = 158 // clears the max-axis label at svgPlotTop+4 below it

	svgPlotTop    = 168
	svgPlotBottom = 288
	svgPlotLeft   = 90
	svgPlotRight  = 608

	svgBreakdownHeadingY    = svgPlotBottom + 32
	svgBreakdownFirstRowY   = svgBreakdownHeadingY + 24
	svgBreakdownRowHeight   = 22
	svgBreakdownRowBudget   = 4 // shown rows; a 5th slot is reserved for the omitted-count summary line
	svgBreakdownLabelRunes  = 22
	svgBreakdownTrackX      = 200
	svgBreakdownTrackWidth  = 160
	svgBreakdownTrackHeight = 8
	svgBreakdownTokensX     = 420
	svgBreakdownCostX       = 608

	svgFontFamily     = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"
	svgAreaGradientID = "areaFill"
)

// svgPalette is the small set of colors and theme-specific shape fills that
// differ between the light and dark card variants (KTD2) — every other
// layout detail is shared, plain Go drawing code.
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

	TileBackground  string
	BadgePositiveBg string
	BadgeNegativeBg string
	// AreaOpacityTop is the trend area fill gradient's top-stop opacity
	// (0-1); the bottom stop is always fully transparent.
	AreaOpacityTop float64
	BreakdownTrack string
	// BarColors rotates across breakdown rows by index so adjacent bars are
	// visually distinct; there are always at least svgBreakdownRowBudget
	// entries so every shown row gets a color.
	BarColors []string
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

	TileBackground:  "#f6f8fa",
	BadgePositiveBg: "#dafbe1",
	BadgeNegativeBg: "#ffebe9",
	AreaOpacityTop:  0.28,
	BreakdownTrack:  "#eaeef2",
	BarColors:       []string{"#0969da", "#8250df", "#1b7c83", "#9a6700"},
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

	TileBackground:  "#161b22",
	BadgePositiveBg: "#0d2818",
	BadgeNegativeBg: "#2d0f0d",
	AreaOpacityTop:  0.32,
	BreakdownTrack:  "#21262d",
	BarColors:       []string{"#58a6ff", "#a371f7", "#39c5cf", "#d29922"},
}

// svgTile is one stat tile (tokens, cost, or streak), positioned at a fixed
// X offset so the three tiles lay out side by side.
type svgTile struct {
	X          int
	Label      string
	Value      string
	Delta      string
	DeltaClass string // "positive", "negative", or "" (no delta, or unchanged)
}

// svgBreakdownRow is one shown breakdown entry, pre-positioned at its row's
// fixed Y so rendering needs no per-row arithmetic. BarWidth is the
// foreground bar's pixel width, proportional to the row's tokens relative
// to the top (largest) shown row; ColorIndex selects BarColors.
type svgBreakdownRow struct {
	Y          int
	Label      string
	Tokens     string
	Cost       string
	BarWidth   int
	ColorIndex int
}

// svgTextLine is a single positioned line of text — used for the
// omitted-entries breakdown summary, whose Y depends on how many rows were
// actually shown.
type svgTextLine struct {
	Y    int
	Text string
}

// svgTrend is the trend chart's data, pre-scaled to the fixed plot
// rectangle (svgPlotTop/Bottom/Left/Right). NoData covers a user with real
// history but nothing in the current trailing window (distinct from
// svgCardData.NoData, which covers no history at all); Single covers the
// degenerate one-data-point case, mirroring trendLines' own single-day
// branch: a labeled point instead of a one-point polyline.
type svgTrend struct {
	NoData        bool
	NoDataMessage string

	Single         bool
	PointX, PointY int
	PointText      string

	// X/Y are the polyline's plotted coordinates, one pair per date,
	// already scaled into the plot rectangle. Empty unless NoData/Single
	// are both false.
	X, Y                 []int
	StartLabel, EndLabel string
	MaxLabel, MinLabel   string
}

// svgCardData is the card's fully computed, palette-independent content —
// RenderSVG builds it once and renders it twice, swapping only the palette,
// rather than recomputing text/positions per theme.
type svgCardData struct {
	Width, Height int
	Title         string

	// NoData covers a brand-new adopter: no history at all, merged or
	// otherwise. Hides Tiles/Trend/Breakdown behind one message.
	NoData        bool
	NoDataMessage string

	Tiles            []svgTile
	BreakdownHeading string
	BreakdownRows    []svgBreakdownRow
	OmittedLine      *svgTextLine
	// BreakdownNoDataMessage, when non-empty, replaces BreakdownRows and
	// OmittedLine — set when real history exists but the current window has
	// none, alongside Trend.NoData (see buildSVGCardData).
	BreakdownNoDataMessage string
	Trend                  svgTrend
	LastUpdated            string
}

// noWindowDataMessage covers a user with real merged history but zero rows
// in the current trailing window (e.g. inactive for longer than the
// window) — distinct from noDataMessage, which implies a brand-new
// adopter. Reusing noDataMessage for this case would misrepresent existing
// history as a first-run state.
const noWindowDataMessage = "No usage in this window."

// RenderSVG composes ds, sum, and mode into light and dark SVG variants of
// the dashboard card (R1, R4): the same title/stat-duration, headline
// stats with deltas, trend chart, streak, and breakdown truncation Render
// already shows (R3) — reusing this package's own grouping/formatting
// helpers directly (KTD4) — laid out on one fixed-size canvas per theme
// rather than Render's auto-sizing box (see the package-level layout
// constants). hasHistory distinguishes a brand-new adopter (no rows in the
// merged dataset at all, before window-filtering) from a returning user
// whose current window (ds) happens to be empty — the latter still shows
// real headline stats/streak, with only the trend/breakdown sections
// substituting a window-scoped no-data message.
func RenderSVG(ds snapshot.MergedDataset, hasHistory bool, sum summary.Summary, mode config.BreakdownMode, breakdownLimit int, renderedAt time.Time) (light, dark string, err error) {
	data := buildSVGCardData(ds, hasHistory, sum, mode, breakdownLimit, renderedAt)
	return renderSVGCard(data, svgLightPalette), renderSVGCard(data, svgDarkPalette), nil
}

// AltText renders ds/sum's headline stats as one plain-text sentence (R8),
// consumed by a later unit's <img alt> attribute. hasHistory false (a
// brand-new adopter, mirroring RenderSVG's own parameter) explicitly names
// the "no data yet" state rather than a misleadingly precise all-zero
// sentence like "Tokens: 0 Cost: $0.00 Streak: 0 days".
func AltText(ds snapshot.MergedDataset, hasHistory bool, sum summary.Summary) string {
	if !hasHistory {
		return CardTitle + " — " + noDataMessage
	}
	return fmt.Sprintf("%s. %s. %s.", titleLine(sum), summaryLine(sum), streakLine(sum))
}

func buildSVGCardData(ds snapshot.MergedDataset, hasHistory bool, sum summary.Summary, mode config.BreakdownMode, breakdownLimit int, renderedAt time.Time) svgCardData {
	data := svgCardData{
		Width:       svgWidth,
		Height:      svgHeight,
		Title:       titleLine(sum),
		LastUpdated: lastUpdatedLine(renderedAt),
	}

	if !hasHistory {
		data.NoData = true
		data.NoDataMessage = noDataMessage
		return data
	}

	data.Tiles = []svgTile{
		{
			X: svgTileX(0), Label: "TOKENS",
			Value: formatTokens(sum.TotalTokens),
			Delta: deltaText(sum.TokenChangePct), DeltaClass: deltaClass(sum.TokenChangePct),
		},
		{
			X: svgTileX(1), Label: "COST",
			Value: fmt.Sprintf("$%.2f", sum.TotalCost),
			Delta: deltaText(sum.CostChangePct), DeltaClass: deltaClass(sum.CostChangePct),
		},
		{
			X: svgTileX(2), Label: "STREAK",
			Value: streakTileValue(sum.Streak),
		},
	}
	data.BreakdownHeading = breakdownHeading(mode)

	if len(ds.Rows) == 0 {
		data.Trend = svgTrend{NoData: true, NoDataMessage: noWindowDataMessage}
		data.BreakdownNoDataMessage = noWindowDataMessage
		return data
	}
	data.BreakdownRows, data.OmittedLine = buildSVGBreakdown(ds, mode, breakdownLimit)
	data.Trend = buildSVGTrend(ds)
	return data
}

// svgTileX returns the fixed X offset of the index'th stat tile (0-2), the
// three tiles evenly filling the space between svgMarginX and svgPlotRight.
func svgTileX(index int) int {
	return svgMarginX + index*(svgTileWidth+svgTileGap)
}

// streakTileValue renders the streak tile's value, appending a fire emoji
// once a streak is actually underway — a low-cost bit of the "stat-tile
// dashboard" mockup's visual flavor, skipped at zero so an idle card
// doesn't celebrate nothing.
func streakTileValue(days int) string {
	v := streakValue(days)
	if days > 0 {
		v += " \U0001F525"
	}
	return v
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
// plus a proportional bar width, since the SVG draws a bar-style row rather
// than one line of text. BarWidth is scaled relative to the top (largest,
// since entries are sorted descending) shown row's tokens.
func buildSVGBreakdown(ds snapshot.MergedDataset, mode config.BreakdownMode, limit int) (rows []svgBreakdownRow, omitted *svgTextLine) {
	entries := groupBreakdown(ds.Rows, mode)
	shown, rest := splitBreakdownEntries(entries, effectiveBreakdownLimit(limit))

	var maxTokens int64
	if len(shown) > 0 {
		maxTokens = shown[0].Tokens
	}

	for i, e := range shown {
		var barWidth int
		if maxTokens > 0 {
			barWidth = int(float64(svgBreakdownTrackWidth) * float64(e.Tokens) / float64(maxTokens))
		}
		rows = append(rows, svgBreakdownRow{
			Y:          svgBreakdownFirstRowY + i*svgBreakdownRowHeight,
			Label:      truncateLabel(e.Label, svgBreakdownLabelRunes),
			Tokens:     formatTokens(e.Tokens),
			Cost:       fmt.Sprintf("$%.2f", e.Cost),
			BarWidth:   barWidth,
			ColorIndex: i,
		})
	}

	if len(rest) == 0 {
		return rows, nil
	}
	tokens, cost := sumBreakdownEntries(rest)
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
			PointX:    (svgPlotLeft + svgPlotRight) / 2,
			PointY:    (svgPlotTop + svgPlotBottom) / 2,
			PointText: fmt.Sprintf("%s: %s tokens", shortDate(dates[0]), formatTokens(int64(tokens[0]))),
		}
	}

	minTok, maxTok := slices.Min(tokens), slices.Max(tokens)
	span := maxTok - minTok

	x := make([]int, len(dates))
	y := make([]int, len(dates))
	xStep := float64(svgPlotRight-svgPlotLeft) / float64(len(dates)-1)
	for i, v := range tokens {
		x[i] = svgPlotLeft + int(float64(i)*xStep)
		y[i] = (svgPlotTop + svgPlotBottom) / 2
		if span > 0 {
			y[i] = svgPlotBottom - int((v-minTok)/span*float64(svgPlotBottom-svgPlotTop))
		}
	}

	return svgTrend{
		X: x, Y: y,
		StartLabel: shortDate(dates[0]),
		EndLabel:   shortDate(dates[len(dates)-1]),
		MaxLabel:   formatTokens(int64(maxTok)),
		MinLabel:   formatTokens(int64(minTok)),
	}
}

// renderSVGCard draws data with palette's colors using svgo's programmatic
// API and returns the finished document as a string.
func renderSVGCard(data svgCardData, palette svgPalette) string {
	var buf bytes.Buffer
	canvas := svg.New(&buf)
	canvas.Start(data.Width, data.Height,
		fmt.Sprintf(`viewBox="0 0 %d %d"`, data.Width, data.Height),
		`role="img"`,
		fmt.Sprintf(`font-family="%s"`, svgFontFamily),
	)
	canvas.Title(data.Title)
	canvas.Rect(0, 0, data.Width, data.Height, fmt.Sprintf(`fill="%s"`, palette.Background))
	canvas.Roundrect(1, 1, data.Width-2, data.Height-2, 12, 12,
		`fill="none"`, fmt.Sprintf(`stroke="%s"`, palette.Border), `stroke-width="2"`)
	canvas.Text(svgMarginX, 40, data.Title, textAttrs(22, 600, palette.Title)...)
	canvas.Line(svgMarginX, 56, svgPlotRight, 56,
		fmt.Sprintf(`stroke="%s"`, palette.Border), `stroke-width="1"`)

	if data.NoData {
		canvas.Text(data.Width/2, 260, data.NoDataMessage,
			textAttrs(16, 0, palette.Muted, `text-anchor="middle"`)...)
		canvas.End()
		return buf.String()
	}

	renderTiles(canvas, data.Tiles, palette)
	renderTrend(canvas, data.Trend, palette)
	renderBreakdown(canvas, data, palette)

	canvas.Text(svgMarginX, 504, data.LastUpdated, textAttrs(11, 0, palette.Muted)...)
	canvas.End()
	return buf.String()
}

// textAttrs builds the raw SVG presentation attributes svgo's Text/Span
// accept for a text run — font-size, fill, an optional font-weight, plus
// any extra attributes (e.g. text-anchor, letter-spacing) verbatim. Each
// returned string contains "=", so svgo passes it through unmodified
// rather than wrapping it as a single style="..." attribute.
func textAttrs(size, weight int, fill string, extra ...string) []string {
	attrs := []string{fmt.Sprintf(`font-size="%d"`, size), fmt.Sprintf(`fill="%s"`, fill)}
	if weight > 0 {
		attrs = append(attrs, fmt.Sprintf(`font-weight="%d"`, weight))
	}
	return append(attrs, extra...)
}

// renderTiles draws the stat-tile row: a rounded-rect background, label,
// bold value, and — when the tile carries a window-over-window delta — a
// colored pill badge stacked below the value. The badge sits on its own
// line (rather than inline after the value, as the earlier plain-text
// design did) because SVG has no text-measurement primitive: an inline
// badge would need to know the value text's rendered width to avoid
// overlapping it, which isn't available without real font metrics.
func renderTiles(canvas *svg.SVG, tiles []svgTile, palette svgPalette) {
	for _, tile := range tiles {
		canvas.Roundrect(tile.X, svgTileTop, svgTileWidth, svgTileHeight, 8, 8,
			fmt.Sprintf(`fill="%s"`, palette.TileBackground))

		labelX := tile.X + svgTilePadX
		canvas.Text(labelX, svgTileTop+svgTileLabelDY, tile.Label,
			textAttrs(11, 600, palette.Muted, `letter-spacing="1"`)...)
		canvas.Text(labelX, svgTileTop+svgTileValueDY, tile.Value,
			textAttrs(22, 700, palette.Text)...)

		if tile.Delta == "" {
			continue
		}
		badgeBg, badgeFg := badgeColors(palette, tile.DeltaClass)
		badgeWidth := deltaBadgeWidth(tile.Delta)
		badgeX := tile.X + svgTileWidth - svgTilePadX - badgeWidth
		badgeY := svgTileTop + svgTileBadgeDY
		canvas.Roundrect(badgeX, badgeY, badgeWidth, svgTileBadgeH, svgTileBadgeRX, svgTileBadgeRX,
			fmt.Sprintf(`fill="%s"`, badgeBg))
		canvas.Text(badgeX+8, badgeY+14, tile.Delta, textAttrs(10, 600, badgeFg)...)
	}
}

// badgeColors picks a delta badge's background/foreground colors for
// class ("positive", "negative", or "" — an exact-zero delta, which
// deltaText still renders as "+0%"). The neutral case reuses the
// breakdown row track color as its background rather than adding a third
// badge-background palette field for one edge case.
func badgeColors(palette svgPalette, class string) (bg, fg string) {
	switch class {
	case "positive":
		return palette.BadgePositiveBg, palette.Positive
	case "negative":
		return palette.BadgeNegativeBg, palette.Negative
	default:
		return palette.BreakdownTrack, palette.Muted
	}
}

// deltaBadgeWidth estimates a delta badge's pixel width from its character
// count. SVG has no text-measurement primitive, but a delta's character
// set (digits, +/-, %) is narrow enough that a fixed per-character
// estimate at the badge's 10px font keeps the pill snug without real font
// metrics.
func deltaBadgeWidth(text string) int {
	const paddingX = 8
	const avgCharWidth = 7
	return paddingX*2 + utf8.RuneCountInString(text)*avgCharWidth
}

// renderTrend draws the "TREND" heading, the plot's baseline, and either
// the no-window-data message, a single labeled point, or a gradient-filled
// area plus polyline for a real multi-day series.
func renderTrend(canvas *svg.SVG, trend svgTrend, palette svgPalette) {
	canvas.Text(svgMarginX, svgTrendHeadingY, "TREND", textAttrs(12, 600, palette.Muted, `letter-spacing="1"`)...)
	canvas.Line(svgPlotLeft, svgPlotBottom, svgPlotRight, svgPlotBottom,
		fmt.Sprintf(`stroke="%s"`, palette.Grid), `stroke-width="1"`)

	switch {
	case trend.NoData:
		canvas.Text((svgPlotLeft+svgPlotRight)/2, (svgPlotTop+svgPlotBottom)/2, trend.NoDataMessage,
			textAttrs(13, 0, palette.Muted, `text-anchor="middle"`)...)
	case trend.Single:
		canvas.Circle(trend.PointX, trend.PointY, 5, fmt.Sprintf(`fill="%s"`, palette.Accent))
		canvas.Text(trend.PointX, trend.PointY-20, trend.PointText,
			textAttrs(13, 0, palette.Text, `text-anchor="middle"`)...)
	default:
		canvas.Def()
		canvas.LinearGradient(svgAreaGradientID, 0, 0, 0, 100, []svg.Offcolor{
			{Offset: 0, Color: palette.Accent, Opacity: palette.AreaOpacityTop},
			{Offset: 100, Color: palette.Accent, Opacity: 0},
		})
		canvas.DefEnd()

		areaX, areaY := areaPolygonPoints(trend.X, trend.Y, svgPlotBottom)
		canvas.Polygon(areaX, areaY, fmt.Sprintf(`fill="url(#%s)"`, svgAreaGradientID))
		canvas.Polyline(trend.X, trend.Y,
			`fill="none"`, fmt.Sprintf(`stroke="%s"`, palette.Accent), `stroke-width="3"`,
			`stroke-linecap="round"`, `stroke-linejoin="round"`)

		canvas.Text(svgPlotLeft, 306, trend.StartLabel, textAttrs(12, 0, palette.Muted)...)
		canvas.Text(svgPlotRight, 306, trend.EndLabel,
			textAttrs(12, 0, palette.Muted, `text-anchor="end"`)...)
		canvas.Text(svgPlotLeft-8, svgPlotTop+4, trend.MaxLabel,
			textAttrs(12, 0, palette.Muted, `text-anchor="end"`)...)
		canvas.Text(svgPlotLeft-8, svgPlotBottom, trend.MinLabel,
			textAttrs(12, 0, palette.Muted, `text-anchor="end"`)...)
	}
}

// areaPolygonPoints closes trend line points (x, y) into a fillable
// polygon by dropping straight down from the last point to baseline, then
// back along baseline to below the first point — SVG implicitly closes
// the remaining edge back to (x[0], y[0]).
func areaPolygonPoints(x, y []int, baseline int) (px, py []int) {
	n := len(x)
	px = make([]int, n+2)
	py = make([]int, n+2)
	copy(px, x)
	copy(py, y)
	px[n], px[n+1] = x[n-1], x[0]
	py[n], py[n+1] = baseline, baseline
	return px, py
}

// renderBreakdown draws the breakdown heading and either the
// window-scoped no-data message or one bar-style row per shown entry (a
// background track plus a proportional foreground bar, rotating through
// palette.BarColors) followed by the omitted-entries summary line, if any.
func renderBreakdown(canvas *svg.SVG, data svgCardData, palette svgPalette) {
	canvas.Text(svgMarginX, svgBreakdownHeadingY, data.BreakdownHeading,
		textAttrs(12, 600, palette.Muted, `letter-spacing="1"`)...)

	if data.BreakdownNoDataMessage != "" {
		canvas.Text(svgMarginX, svgBreakdownFirstRowY, data.BreakdownNoDataMessage,
			textAttrs(13, 0, palette.Muted)...)
		return
	}

	for _, row := range data.BreakdownRows {
		canvas.Text(svgMarginX, row.Y, row.Label, textAttrs(13, 0, palette.Text)...)

		trackY := row.Y - svgBreakdownTrackHeight - 1
		canvas.Roundrect(svgBreakdownTrackX, trackY, svgBreakdownTrackWidth, svgBreakdownTrackHeight, 4, 4,
			fmt.Sprintf(`fill="%s"`, palette.BreakdownTrack))
		if row.BarWidth > 0 {
			barColor := palette.BarColors[row.ColorIndex%len(palette.BarColors)]
			canvas.Roundrect(svgBreakdownTrackX, trackY, row.BarWidth, svgBreakdownTrackHeight, 4, 4,
				fmt.Sprintf(`fill="%s"`, barColor))
		}

		canvas.Text(svgBreakdownTokensX, row.Y, row.Tokens,
			textAttrs(13, 0, palette.Muted, `text-anchor="end"`)...)
		canvas.Text(svgBreakdownCostX, row.Y, row.Cost,
			textAttrs(13, 0, palette.Muted, `text-anchor="end"`)...)
	}

	if data.OmittedLine != nil {
		canvas.Text(svgMarginX, data.OmittedLine.Y, data.OmittedLine.Text, textAttrs(13, 0, palette.Muted)...)
	}
}
