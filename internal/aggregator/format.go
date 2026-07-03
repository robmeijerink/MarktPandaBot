package aggregator

import (
	"fmt"
	"math"
	"strings"
)

// Telegram renders messages in a proportional font, so space-padding only lines
// up inside a fixed-width (```) code block. The formatters here build the aligned
// per-venue blocks; the engine wraps the exchange tables in a code fence.

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

// formatExchangeBlock renders one venue's stats as aligned monospace rows (no
// inner emojis, so the value columns line up across both venues). The leading
// emoji sits only on the header line where it cannot skew the columns below.
func formatExchangeBlock(emoji, name string, s legStats, funding, oi, oiDelta float64) string {
	return fmt.Sprintf(
		"%s %-5s Total ~%s · %.2f ₿\n"+
			"   %-8s%10s    %-8s%9s\n"+
			"   %-8s%10d    %-8s~%s %s\n"+
			"   %-8s%s – %s\n"+
			"   %-8s%+.4f%%\n"+
			"   %-8s%s (Δ %s)",
		emoji, name, humanUSD(s.volUSDT), s.volBTC,
		"Longs", "~"+humanUSD(s.longUSDT), "Shorts", "~"+humanUSD(s.shortUSDT),
		"Orders", s.count, "Biggest", humanUSD(s.biggestUSDT), s.biggestSide,
		"Range", comma(s.min), comma(s.max),
		"Funding", funding*100,
		"OI", humanUSD(oi), signedUSD(oiDelta),
	)
}
