package sweep

import (
	"strings"
	"testing"
	"time"
)

// The alert shows every swept level (most significant first), the wick, volume,
// liquidations per venue and the invalidation, and contains nothing that would break
// Telegram's legacy Markdown.
func TestBuildAlert(t *testing.T) {
	at := time.Date(2026, 9, 14, 13, 5, 0, 0, time.UTC)
	e := sweepEvent{
		bar: Bar{Start: at, Close: 63704.4},
		low: true,
		levels: []*level{
			{price: 63480, born: at.Add(-9 * time.Hour), kind: kindHourSwing, touches: 2, low: true},
			{price: 63520, born: at.Add(-13 * time.Hour), kind: kindPrevDay, touches: 1, low: true},
		},
		extreme:    63410.2,
		wickATR:    2.14,
		wickRatio:  0.68,
		volRatio:   3.4,
		liqUSD:     1234000,
		liqVenues:  [venueCount]float64{934000, 300000},
		liqCovered: [venueCount]bool{true, true},
	}
	msg := buildAlert(DefaultConfig(), e)
	want := []string{
		"🧹 *LIQUIDITY SWEEP* 🟢 *BULLISH*",
		"🎯 Swept *previous day low* $63,520\n",
		"      + *1h swing low* $63,480 (tested 2×, formed 9h ago)",
		"💲 BTC $63,704 · 5m candle closed back above",
		"🕯 Wick to $63,410 · 2.1× ATR · 68% of the candle",
		"📊 Volume 3.4× the 4h median",
		"💥 *$1.2M longs liquidated* (Bybit $934k · OKX $300k)",
		"🛑 Invalidated below $63,410",
	}
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("alert is missing %q:\n%s", w, msg)
		}
	}
	for _, bad := range []string{"_", "`", "["} {
		if strings.Contains(msg, bad) {
			t.Errorf("alert contains %q, which breaks Telegram Markdown:\n%s", bad, msg)
		}
	}
	if strings.Count(msg, "*")%2 != 0 {
		t.Errorf("unbalanced bold markers:\n%s", msg)
	}

	e.liqCovered[venueOKX] = false
	if msg := buildAlert(DefaultConfig(), e); !strings.Contains(msg, "(Bybit $934k · OKX n/a)") {
		t.Errorf("a venue whose feed was down must show n/a:\n%s", msg)
	}
}

func TestComma(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999.4: "999", 63704.6: "63,705", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := comma(in); got != want {
			t.Errorf("comma(%v) = %q, want %q", in, got, want)
		}
	}
}
