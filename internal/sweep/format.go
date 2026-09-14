package sweep

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// buildAlert renders the Telegram message (legacy Markdown: *bold* only, so no value
// may contain '*', '_', '`' or '['). It opens with the 🧹 prefix so it stands apart
// from the other alerts in the feed.
func buildAlert(cfg Config, e sweepEvent) string {
	dot, word, back, beyond := "🟢", "BULLISH", "above", "below"
	if !e.low {
		dot, word, back, beyond = "🔴", "BEARISH", "below", "above"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧹 *LIQUIDITY SWEEP* %s *%s*\n\n", dot, word)

	levels := sortedLevels(e.levels)
	for i, lv := range levels {
		prefix := "🎯 Swept"
		if i > 0 {
			prefix = "      +"
		}
		fmt.Fprintf(&sb, "%s *%s* $%s%s\n", prefix, lv.kind.label(e.low), comma(lv.price), levelNote(lv, e.bar.Start))
	}
	fmt.Fprintf(&sb, "💲 BTC $%s · %s candle closed back %s\n\n", comma(e.bar.Close), cfg.Timeframe, back)

	fmt.Fprintf(&sb, "🕯 Wick to $%s · %.1f× ATR · %.0f%% of the candle\n", comma(e.extreme), e.wickATR, e.wickRatio*100)
	fmt.Fprintf(&sb, "📊 Volume %.1f× the %s median\n", e.volRatio, humanDuration(time.Duration(cfg.VolumeLookback)*cfg.step()))
	fmt.Fprintf(&sb, "💥 %s\n\n", liquidationLine(e))

	fmt.Fprintf(&sb, "🛑 Invalidated %s $%s\n", beyond, comma(e.extreme))
	sb.WriteString("ℹ️ Stops were run and the level reclaimed — a sweep, not a guaranteed reversal.")
	return sb.String()
}

// liquidationLine reports the swept side's liquidations with the per-venue split,
// marking a venue whose feed was down as n/a.
func liquidationLine(e sweepEvent) string {
	if !e.liqCovered[venueBybit] && !e.liqCovered[venueOKX] {
		return "Liquidations: feeds offline"
	}
	parts := make([]string, 0, venueCount)
	for v := range venueCount {
		if e.liqCovered[v] {
			parts = append(parts, fmt.Sprintf("%s %s", venueNames[v], humanUSD(e.liqVenues[v])))
		} else {
			parts = append(parts, venueNames[v]+" n/a")
		}
	}
	return fmt.Sprintf("*%s %s liquidated* (%s)", humanUSD(e.liqUSD), flushedSide(e.low), strings.Join(parts, " · "))
}

// sortedLevels orders swept levels by significance: week, then day, then 1h swings
// (more touches first).
func sortedLevels(levels []*level) []*level {
	out := append([]*level(nil), levels...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].kind != out[j].kind {
			return out[i].kind > out[j].kind
		}
		return out[i].touches > out[j].touches
	})
	return out
}

func levelNote(lv *level, at time.Time) string {
	notes := []string{}
	if lv.touches > 1 {
		notes = append(notes, fmt.Sprintf("tested %d×", lv.touches))
	}
	if lv.kind == kindHourSwing {
		notes = append(notes, "formed "+humanDuration(at.Sub(lv.born))+" ago")
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, ", ") + ")"
}

// describeLevels is the short plain-text form used in journal lines.
func describeLevels(levels []*level, low bool) string {
	names := make([]string, 0, len(levels))
	for _, lv := range sortedLevels(levels) {
		names = append(names, fmt.Sprintf("%s %.1f", lv.kind.label(low), lv.price))
	}
	return strings.Join(names, " + ")
}

// humanUSD formats a dollar amount compactly: $850k, $1.2M.
func humanUSD(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e9:
		return fmt.Sprintf("$%.2fB", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("$%.1fM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("$%.0fk", v/1e3)
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}

// comma renders a price rounded to whole dollars with thousands separators.
func comma(v float64) string {
	s := fmt.Sprintf("%.0f", math.Abs(v))
	var out []byte
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if v < 0 {
		return "-" + string(out)
	}
	return string(out)
}

// humanDuration renders 45m, 4h or 2d.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
