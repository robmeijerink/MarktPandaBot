package aggregator

import (
	"strings"
	"testing"
)

func TestComma(t *testing.T) {
	cases := map[float64]string{
		0: "0", 633: "633", 63681: "63,681", 1234567: "1,234,567", -40500000: "-40,500,000",
	}
	for in, want := range cases {
		if got := comma(in); got != want {
			t.Errorf("comma(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderSamples prints a fully-rendered raw liquidation alert so alignment can
// be eyeballed in a monospace terminal (go test -v -run RenderSamples). It is not an
// assertion test beyond confirming the tables are wrapped in a code fence.
func TestRenderSamples(t *testing.T) {
	bybit := legStats{
		count: 633, volBTC: 167.55, volUSDT: 10_600_000,
		longUSDT: 10_600_000, shortUSDT: 0,
		min: 63285, max: 63740, biggestUSDT: 2_000_000, biggestSide: "long",
	}
	okx := legStats{
		count: 67, volBTC: 7.56, volUSDT: 481_000,
		longUSDT: 481_000, shortUSDT: 0,
		min: 63366, max: 64071, biggestUSDT: 140_000, biggestSide: "long",
	}

	alert := "🚨 *LIQUIDATION ALERT*\n\n" +
		"🔄 Potential REVERSAL UP — long capitulation\n" +
		"_OI -0.95%  ·  BTC $" + comma(63681) + "  ·  -4.7% 24h_\n" +
		"⚠️ Combined ~" + humanUSD(11_100_000) + " liquidated in the last 5m\n\n" +
		"```\n" +
		formatExchangeBlock("📍", "BYBIT", bybit, -0.000018, 3_730_000_000, -40_500_000) +
		"\n\n" +
		formatExchangeBlock("🌐", "OKX", okx, 0.000092, 2_420_000_000, -18_000_000) +
		"\n```"

	t.Log("\n" + alert)

	if !strings.Contains(alert, "```") {
		t.Fatal("alert not wrapped in a code fence")
	}
}
