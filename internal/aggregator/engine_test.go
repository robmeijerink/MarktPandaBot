package aggregator

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAggregator_Concurrency(t *testing.T) {
	aggr := &Aggregator{}
	var wg sync.WaitGroup

	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aggr.AddEvent(LiquidationEvent{
				Exchange: "okx",
				Symbol:   "BTC-USDT-SWAP",
				Price:    74000.50,
				Qty:      1.5,
				Side:     "SELL",
			})
		}()
	}
	wg.Wait()

	events := aggr.ExtractAndClear()
	if len(events) != 1000 {
		t.Fatalf("expected 1000 events, got %d", len(events))
	}

	emptyEvents := aggr.ExtractAndClear()
	if len(emptyEvents) != 0 {
		t.Fatalf("expected 0 events after clear, got %d", len(emptyEvents))
	}
}

func TestDynamicThreshold(t *testing.T) {
	// Calm/low-turnover market: baseline tiny, volatility below ref -> floor wins.
	th, mult := dynamicThreshold(50_000_000 /*$50M 24h turnover*/, 70000 /*price*/, 70700, 69300 /*~2% range*/)
	if th != ThresholdFloorUSDT {
		t.Errorf("calm market: expected floor %.0f, got %.0f (mult %.2f)", ThresholdFloorUSDT, th, mult)
	}
	if mult != 1.0 {
		t.Errorf("calm market: expected mult 1.0, got %.2f", mult)
	}

	// Very volatile market: range >> ref -> multiplier should be capped.
	_, mult2 := dynamicThreshold(50_000_000, 70000, 84000, 56000 /*40% range*/)
	if mult2 != MaxVolatilityMultiple {
		t.Errorf("volatile market: expected capped mult %.2f, got %.2f", MaxVolatilityMultiple, mult2)
	}

	// High-turnover regime should lift the threshold above the floor.
	thHigh, _ := dynamicThreshold(200_000_000_000 /*$200B 24h turnover*/, 70000, 75000, 65000)
	if thHigh <= ThresholdFloorUSDT {
		t.Errorf("high-turnover regime: expected threshold above floor, got %.0f", thHigh)
	}
}

func TestClassifySignal(t *testing.T) {
	const oi = 6_000_000_000.0
	cases := []struct {
		name              string
		oiDelta           float64
		longUSDT          float64
		shortUSDT         float64
		wantLabelContains string
	}{
		{"drop, longs dominant", -60_000_000 /*-1.0%*/, 800000, 100000, "REVERSAL UP"},
		{"drop, shorts dominant", -60_000_000, 100000, 800000, "REVERSAL DOWN"},
		{"rise, longs dominant", +60_000_000 /*+1.0%*/, 800000, 100000, "CONTINUATION DOWN"},
		{"rise, shorts dominant", +60_000_000, 100000, 800000, "CONTINUATION UP"},
		{"moderate drop below bar -> unclear", -30_000_000 /*-0.5%*/, 800000, 100000, "Unclear"},
		{"flat OI -> unclear", +6_000 /*~0%*/, 800000, 100000, "Unclear"},
	}
	for _, c := range cases {
		label, _ := classifySignal(oi, c.oiDelta, c.longUSDT, c.shortUSDT)
		if !strings.Contains(label, c.wantLabelContains) {
			t.Errorf("%s: label %q does not contain %q", c.name, label, c.wantLabelContains)
		}
	}
}

func TestClassifySignal_ConfidenceTiers(t *testing.T) {
	const oi = 6_000_000_000.0

	// Moderate swing (1.0%, between 0.7% and 1.5%) -> "Potential", not "Likely".
	moderate, _ := classifySignal(oi, -60_000_000, 800000, 100000)
	if !strings.Contains(moderate, "Potential") || strings.Contains(moderate, "Likely") {
		t.Errorf("moderate swing: want 'Potential', got %q", moderate)
	}

	// Strong swing (2.0%, >= 1.5%) -> "Likely".
	strong, _ := classifySignal(oi, -120_000_000, 800000, 100000)
	if !strings.Contains(strong, "Likely") {
		t.Errorf("strong swing: want 'Likely', got %q", strong)
	}
}

// TestComputeStats_LongFlush reproduces the reported alert (a -4.5% dump where
// LONGS were liquidated on both venues). After side normalisation the engine
// receives SELL on both legs (OKX natively, Bybit "Buy" -> SELL via
// bybit.normaliseSide), so both blocks, both biggest labels, and the reversal
// direction must all read LONG / "REVERSAL UP".
func TestComputeStats_LongFlush(t *testing.T) {
	data := []LiquidationEvent{
		{Exchange: "okx", Price: 69600, Qty: 5.2, Side: "SELL"},   // 5.2 ₿ long liq
		{Exchange: "bybit", Price: 69300, Qty: 3.1, Side: "SELL"}, // ~$215k long liq (Qty in BTC)
		{Exchange: "bybit", Price: 69310, Qty: 3.9, Side: "SELL"}, // ~$270k long liq
	}
	okx, bybit := computeStats(data)

	if okx.longUSDT <= 0 || okx.shortUSDT != 0 {
		t.Errorf("okx: expected all longs, got long=%.0f short=%.0f", okx.longUSDT, okx.shortUSDT)
	}
	if bybit.longUSDT <= 0 || bybit.shortUSDT != 0 {
		t.Errorf("bybit: expected all longs, got long=%.0f short=%.0f", bybit.longUSDT, bybit.shortUSDT)
	}
	// Both legs must now report a BTC total.
	if okx.volBTC <= 0 || bybit.volBTC <= 0 {
		t.Errorf("expected BTC totals on both legs, got okx=%.2f bybit=%.2f", okx.volBTC, bybit.volBTC)
	}
	if okx.biggestSide != "long" || bybit.biggestSide != "long" {
		t.Errorf("biggest sides must be long, got okx=%q bybit=%q", okx.biggestSide, bybit.biggestSide)
	}
	// -60M on $6B OI = -1.0%, past the 0.7% bar -> a real reversal.
	label, _ := classifySignal(6e9, -60e6, okx.longUSDT+bybit.longUSDT, okx.shortUSDT+bybit.shortUSDT)
	if !strings.Contains(label, "REVERSAL UP") {
		t.Errorf("expected REVERSAL UP, got %q", label)
	}
}

// TestComputeStats_ShortFlush is the mirror: a squeeze where SHORTS are
// liquidated (OKX BUY, Bybit "Sell" -> BUY) must read short / "REVERSAL DOWN".
func TestComputeStats_ShortFlush(t *testing.T) {
	data := []LiquidationEvent{
		{Exchange: "okx", Price: 72000, Qty: 4.0, Side: "BUY"},
		{Exchange: "bybit", Price: 72010, Qty: 4.2, Side: "BUY"}, // Qty in BTC
	}
	okx, bybit := computeStats(data)

	if okx.shortUSDT <= 0 || okx.longUSDT != 0 {
		t.Errorf("okx: expected all shorts, got long=%.0f short=%.0f", okx.longUSDT, okx.shortUSDT)
	}
	if bybit.shortUSDT <= 0 || bybit.longUSDT != 0 {
		t.Errorf("bybit: expected all shorts, got long=%.0f short=%.0f", bybit.longUSDT, bybit.shortUSDT)
	}
	if okx.biggestSide != "short" || bybit.biggestSide != "short" {
		t.Errorf("biggest sides must be short, got okx=%q bybit=%q", okx.biggestSide, bybit.biggestSide)
	}
	label, _ := classifySignal(6e9, -60e6, okx.longUSDT+bybit.longUSDT, okx.shortUSDT+bybit.shortUSDT)
	if !strings.Contains(label, "REVERSAL DOWN") {
		t.Errorf("expected REVERSAL DOWN, got %q", label)
	}
}

func TestEvaluationCadence(t *testing.T) {
	if EvaluationInterval != 5*time.Minute {
		t.Errorf("evaluation interval must be 5m, got %s", EvaluationInterval)
	}
	// windowsPerDay must stay consistent with the interval (288 for 5-min),
	// since the dynamic threshold slices 24h turnover by it.
	if windowsPerDay != 288 {
		t.Errorf("windowsPerDay must be 288 for a 5m interval, got %v", windowsPerDay)
	}
}

func TestMarketState_ThreadSafety(t *testing.T) {
	state := &MarketState{}
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		state.Mu.Lock()
		state.OKXFunding = 0.01
		state.Mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		state.Mu.RLock()
		_ = state.OKXFunding
		state.Mu.RUnlock()
	}()

	wg.Wait()
}
