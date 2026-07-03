package aggregator

import (
	"fmt"
	"math"
	"strings"
)

// The per-venue block is a compact, proportional-font bullet list (no monospace
// alignment), so it reads cleanly on a phone or watch without a code fence.

// comma formats a number as a thousands-grouped integer: 63681 -> "63,681".
func comma(v float64) string {
	return groupThousands(fmt.Sprintf("%.0f", math.Abs(v)), v < 0)
}

func groupThousands(intPart string, neg bool) string {
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(intPart[i])
	}
	return b.String()
}

// formatExchangeBlock renders one venue's stats as a compact bullet list: a header
// line with the venue total, then the long/short split, biggest print + order count,
// the liquidation price range, and funding + OI delta. A colour dot (🔴 long / 🟢
// short) marks the side on the split and the biggest print for an instant read.
func formatExchangeBlock(emoji, name string, s legStats, funding, oi, oiDelta float64) string {
	return fmt.Sprintf(
		"%s %s: ~%s (%.2f ₿)\n"+
			"• Liq: 🔴 ~%s | 🟢 ~%s\n"+
			"• Max: %s ~%s (%d orders)\n"+
			"• Rng: %s - %s\n"+
			"• Fund: %+.4f%% | OI: %s (Δ %s)",
		emoji, name, humanUSD(s.volUSDT), s.volBTC,
		humanUSD(s.longUSDT), humanUSD(s.shortUSDT),
		sideGlyph(s.biggestSide), humanUSD(s.biggestUSDT), s.count,
		comma(s.min), comma(s.max),
		funding*100, humanUSD(oi), signedUSD(oiDelta),
	)
}

// sideGlyph maps a "long"/"short" side to its liquidation-colour dot: red for a
// long liquidation (forced selling), green for a short liquidation (forced buying).
func sideGlyph(side string) string {
	switch side {
	case "long":
		return "🔴"
	case "short":
		return "🟢"
	default:
		return "⚪"
	}
}
