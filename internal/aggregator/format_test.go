package aggregator

import (
	"strings"
	"testing"
	"time"
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
	if got := comma2(64840); got != "64,840.00" {
		t.Errorf("comma2(64840) = %q, want 64,840.00", got)
	}
}

// TestRenderSamples prints fully-rendered samples so alignment can be eyeballed
// in a monospace terminal (go test -v -run RenderSamples). It is not an assertion
// test beyond a basic sanity check.
func TestRenderSamples(t *testing.T) {
	cfg := DefaultConfig()

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

	score := ScoreSetup(cfg, SetupInputs{
		OIChangePct: -0.95, LongLiqUSDT: 11_081_000, ShortLiqUSDT: 0,
		FundingRate: -0.000018, BucketVol: 1, MedianVol: 1, VolMedianOK: true,
	})

	t.Log("\n" + alert + FormatSetupMatrix(cfg, score))

	conf := formatConfirmation(confirmationView{
		minutesWaited: 3, candleStart: time.Date(2026, 6, 4, 14, 0, 0, 0, time.UTC),
		closePx: 64840, closeOK: true, flushRangeHigh: 63740,
		reclaim: true, cvd: metricPass, spot: metricNA, verdict: "Reversal confirmed",
	})
	t.Log("\n" + conf)

	notConfirmed := formatConfirmation(confirmationView{
		minutesWaited: 4, candleStart: time.Date(2026, 6, 4, 2, 0, 0, 0, time.UTC),
		closePx: 62100, closeOK: true, flushRangeHigh: 62347.70,
		reclaim: false, cvd: metricPass, spot: metricPass, verdict: "Not confirmed",
	})
	t.Log("\n" + notConfirmed)

	inconclusive := formatConfirmation(confirmationView{
		minutesWaited: 4, candleStart: time.Date(2026, 6, 4, 2, 0, 0, 0, time.UTC),
		closeOK: false, flushRangeHigh: 62347.70,
		reclaim: false, cvd: metricPass, spot: metricPass,
		verdict: "Inconclusive — no candle close",
	})
	t.Log("\n" + inconclusive)

	// Two-stage watch: early read (pending) then the later reclaim result.
	earlyPending := formatConfirmation(confirmationView{
		minutesWaited: 4, candleStart: time.Date(2026, 6, 4, 2, 0, 0, 0, time.UTC),
		closePx: 62100, closeOK: true, flushRangeHigh: 62347.70,
		reclaim: false, cvd: metricPass, spot: metricPass,
		verdict: "Pending — watching reclaim", watching: true, watchCandles: 3,
	})
	t.Log("\n" + earlyPending)

	reclaimedMsg := formatReclaimResult(reclaimResult{
		reclaimed: true, minutesWaited: 9, candleStart: time.Date(2026, 6, 4, 2, 10, 0, 0, time.UTC),
		closePx: 62500, closeOK: true, flushRangeHigh: 62347.70, candlesWatched: 2,
	})
	t.Log("\n" + reclaimedMsg)

	timedOut := formatReclaimResult(reclaimResult{
		reclaimed: false, minutesWaited: 19, closePx: 61900, closeOK: true,
		flushRangeHigh: 62347.70, candlesWatched: 3,
	})
	t.Log("\n" + timedOut)

	if !strings.Contains(alert, "```") {
		t.Fatal("alert not wrapped in a code fence")
	}
}
