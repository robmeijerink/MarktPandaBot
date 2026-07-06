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

// formatExchangeBlock renders one venue's stats with a leading icon on every line,
// so the icons stack in a single left column for an at-a-glance read. Long and short
// each get their own line, which puts the 🔴/🟢 dots directly under each other (the
// only way to align columns in Telegram's proportional font). Lines: venue total,
// 🔴 longs liquidated, 🟢 shorts liquidated, 🎯 biggest print + order count, 📏 the
// liquidation price range, 💰 funding + OI delta.
func formatExchangeBlock(emoji, name string, s legStats, funding, oi, oiDelta float64) string {
	return fmt.Sprintf(
		"%s %s: ~%s (%.2f ₿)\n"+
			"🔴 Long ~%s\n"+
			"🟢 Short ~%s\n"+
			"🎯 Max ~%s (%d orders)\n"+
			"📏 Rng %s - %s\n"+
			"💰 Fund %+.4f%% · OI %s (Δ %s)",
		emoji, name, humanUSD(s.volUSDT), s.volBTC,
		humanUSD(s.longUSDT),
		humanUSD(s.shortUSDT),
		humanUSD(s.biggestUSDT), s.count,
		comma(s.min), comma(s.max),
		funding*100, humanUSD(oi), signedUSD(oiDelta),
	)
}
