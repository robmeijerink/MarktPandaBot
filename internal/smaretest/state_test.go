package smaretest

import (
	"testing"
	"time"
)

// testMachine builds a machine with a capturing send func and the given config.
func testMachine(t *testing.T, cfg Config) (*machine, *[]string) {
	t.Helper()
	var sent []string
	m := newMachine(cfg, newIndicators(cfg), func(s string) { sent = append(sent, s) })
	return m, &sent
}

func bar(o, h, l, c float64) Bar { return Bar{Open: o, High: h, Low: l, Close: c} }

// ctx assembles a barCtx with explicit indicator readings so the decision logic
// can be tested without engineering 200-bar SMA series.
func ctx(fast, slow, prevFast, prevSlow, band float64, b Bar) barCtx {
	return barCtx{fast: fast, slow: slow, prevFast: prevFast, prevSlow: prevSlow, havePrev: true, band: band, bar: b}
}

// Test 1: cross detection — a sign flip of (SMA21-SMA200) sets the regime; no flip
// leaves it unchanged.
func TestCrossDetection(t *testing.T) {
	cfg := DefaultConfig()

	// prev: fast<slow (short), cur: fast>slow (long) => golden cross => LONG, armed.
	// Price sits clear of the band so this bar only tests the cross, not a touch.
	m, _ := testMachine(t, cfg)
	m.decide(ctx(101, 100, 99, 100, 0.05, bar(101.5, 101.6, 101.2, 101.5)))
	if m.regime != regimeLong || !m.armed || !m.reArmed {
		t.Fatalf("golden cross: regime=%d armed=%v reArmed=%v", m.regime, m.armed, m.reArmed)
	}
	if m.barsSinceCross != 0 {
		t.Fatalf("barsSinceCross should reset to 0 on cross, got %d", m.barsSinceCross)
	}

	// prev: fast>slow, cur: fast<slow => death cross => SHORT (clear of the band).
	m2, _ := testMachine(t, cfg)
	m2.decide(ctx(99, 100, 101, 100, 0.05, bar(98.5, 98.8, 98.4, 98.5)))
	if m2.regime != regimeShort || !m2.armed {
		t.Fatalf("death cross: regime=%d armed=%v", m2.regime, m2.armed)
	}

	// no flip: same side both bars => no regime change from an already-armed long.
	m3, _ := testMachine(t, cfg)
	m3.regime, m3.armed, m3.reArmed = regimeLong, true, true
	m3.barsSinceCross = 5
	m3.decide(ctx(102, 100, 103, 100, 0.05, bar(102, 102.5, 101.9, 102))) // no touch, no flip
	if m3.regime != regimeLong {
		t.Fatalf("no flip should keep regime LONG, got %d", m3.regime)
	}
	if m3.barsSinceCross != 6 {
		t.Fatalf("barsSinceCross should advance to 6, got %d", m3.barsSinceCross)
	}
}

// Test 2: wick-out rejection — a bar whose low pierces the band but closes below
// the 21 SMA does NOT fire a LONG touch; one closing back above DOES. Mirror short.
func TestWickOutRejection(t *testing.T) {
	cfg := DefaultConfig()
	band := 0.05

	// LONG, armed: low pierces (<= fast+band) but close below fast => rejected.
	m, sent := testMachine(t, cfg)
	m.regime, m.armed, m.reArmed = regimeLong, true, true
	m.decide(ctx(100, 90, 100, 90, band, bar(100.02, 100.04, 99.90, 99.95))) // close < fast
	if len(*sent) != 0 {
		t.Fatalf("wick-out (close below SMA) must not fire LONG, got %d alerts", len(*sent))
	}

	// LONG, armed: low touches band and close back above fast => fires.
	m2, sent2 := testMachine(t, cfg)
	m2.regime, m2.armed, m2.reArmed = regimeLong, true, true
	m2.decide(ctx(100, 90, 100, 90, band, bar(100.02, 100.06, 99.97, 100.01))) // low<=100.05, close>=100
	if len(*sent2) != 1 {
		t.Fatalf("valid LONG touch should fire exactly 1 alert, got %d", len(*sent2))
	}

	// SHORT, armed: high pierces but close above fast => rejected.
	m3, sent3 := testMachine(t, cfg)
	m3.regime, m3.armed, m3.reArmed = regimeShort, true, true
	m3.decide(ctx(100, 110, 100, 110, band, bar(99.98, 100.10, 99.96, 100.05))) // close > fast
	if len(*sent3) != 0 {
		t.Fatalf("wick-out (close above SMA) must not fire SHORT, got %d alerts", len(*sent3))
	}

	// SHORT, armed: high touches band and close back below fast => fires.
	m4, sent4 := testMachine(t, cfg)
	m4.regime, m4.armed, m4.reArmed = regimeShort, true, true
	m4.decide(ctx(100, 110, 100, 110, band, bar(99.98, 100.03, 99.94, 99.99)))
	if len(*sent4) != 1 {
		t.Fatalf("valid SHORT touch should fire exactly 1 alert, got %d", len(*sent4))
	}
}

// Test 3: invalidation — a bar reaching the 200 SMA disarms the regime.
func TestInvalidation(t *testing.T) {
	cfg := DefaultConfig()

	// LONG: low reaches the 200 SMA => disarm, no touch alert (EmitInvalidation off).
	m, sent := testMachine(t, cfg)
	m.regime, m.armed, m.reArmed = regimeLong, true, true
	m.decide(ctx(100, 95, 100, 95, 0.05, bar(99, 99, 94.9, 96))) // low 94.9 <= slow 95
	if m.armed {
		t.Fatalf("LONG invalidation should disarm")
	}
	if len(*sent) != 0 {
		t.Fatalf("invalidation with EmitInvalidation=false must be silent, got %d", len(*sent))
	}

	// SHORT: high reaches the 200 SMA => disarm. With EmitInvalidation on => 1 note.
	cfg2 := DefaultConfig()
	cfg2.EmitInvalidation = true
	m2, sent2 := testMachine(t, cfg2)
	m2.regime, m2.armed, m2.reArmed = regimeShort, true, true
	m2.decide(ctx(100, 105, 100, 105, 0.05, bar(101, 105.1, 101, 104))) // high 105.1 >= slow 105
	if m2.armed {
		t.Fatalf("SHORT invalidation should disarm")
	}
	if len(*sent2) != 1 {
		t.Fatalf("invalidation note expected, got %d", len(*sent2))
	}
}

// Test 4: re-arm debounce — after a touch, no second touch fires until a bar closes
// outside the band; firstOnly fires exactly once per regime.
func TestReArmDebounce(t *testing.T) {
	band := 0.05

	cfg := DefaultConfig()
	cfg.ReArmMode = ReArmDebounce
	m, sent := testMachine(t, cfg)
	m.regime, m.armed, m.reArmed = regimeLong, true, true

	// Bar A: valid touch => fires, reArmed=false.
	m.decide(ctx(100, 90, 100, 90, band, bar(100.02, 100.06, 99.97, 100.01)))
	if len(*sent) != 1 || m.reArmed {
		t.Fatalf("bar A should fire and clear reArmed: alerts=%d reArmed=%v", len(*sent), m.reArmed)
	}
	// Bar B: another touch while still not re-armed => no fire.
	m.decide(ctx(100, 90, 100, 90, band, bar(100.01, 100.05, 99.98, 100.0)))
	if len(*sent) != 1 {
		t.Fatalf("bar B must not fire before re-arm, alerts=%d", len(*sent))
	}
	// Bar C: close leaves the band (above fast+band) => re-arm.
	m.decide(ctx(100, 90, 100, 90, band, bar(100.2, 100.3, 100.1, 100.2)))
	if !m.reArmed {
		t.Fatalf("bar C close outside band should re-arm")
	}
	// Bar D: touch again => fires (second alert).
	m.decide(ctx(100, 90, 100, 90, band, bar(100.02, 100.06, 99.97, 100.01)))
	if len(*sent) != 2 {
		t.Fatalf("bar D should fire after re-arm, alerts=%d", len(*sent))
	}

	// firstOnly: fires once then disarms for the regime.
	cfgF := DefaultConfig()
	cfgF.ReArmMode = ReArmFirstOnly
	mf, sentF := testMachine(t, cfgF)
	mf.regime, mf.armed, mf.reArmed = regimeLong, true, true
	mf.decide(ctx(100, 90, 100, 90, band, bar(100.02, 100.06, 99.97, 100.01)))
	if len(*sentF) != 1 || mf.armed {
		t.Fatalf("firstOnly should fire once and disarm: alerts=%d armed=%v", len(*sentF), mf.armed)
	}
	mf.decide(ctx(100, 90, 100, 90, band, bar(100.02, 100.06, 99.97, 100.01)))
	if len(*sentF) != 1 {
		t.Fatalf("firstOnly must not fire again, alerts=%d", len(*sentF))
	}
}

// Test 5: same-bar exclusivity — a single bar never both re-arms and fires.
func TestSameBarExclusivity(t *testing.T) {
	cfg := DefaultConfig()
	band := 0.05

	m, sent := testMachine(t, cfg)
	m.regime, m.armed, m.reArmed = regimeLong, true, true
	// Not re-armed at start; a single bar that both touches and closes outside the
	// band must NOT fire (touch uses armedAtStart) but SHOULD re-arm for next bar.
	m.reArmed = false
	m.decide(ctx(100, 90, 100, 90, band, bar(100.2, 100.3, 99.99, 100.2))) // low touches, close > band
	if len(*sent) != 0 {
		t.Fatalf("bar must not fire while reArmed=false at start, alerts=%d", len(*sent))
	}
	if !m.reArmed {
		t.Fatalf("bar should have re-armed for the next bar")
	}
}

// Test 6: warm boot establishes the regime silently and lets the first live
// qualifying bar fire.
func TestWarmBootSilentThenFire(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FastPeriod = 2
	cfg.SlowPeriod = 3
	cfg.WarmBootBars = 10
	cfg.TouchTolPct = 0.05

	ind := newIndicators(cfg)
	var sent []string
	m := newMachine(cfg, ind, func(s string) { sent = append(sent, s) })

	// Hydrate a clearly-uptrending series so SMA2 > SMA3 (LONG regime).
	closes := []float64{100, 101, 102, 103, 104, 105}
	base := int64(1_700_000_000_000)
	for i, c := range closes {
		ind.push(Bar{BucketStart: msTime(base + int64(i)*180000), Open: c, High: c, Low: c, Close: c})
	}

	// Exercise the real silent-arming path used by warm boot.
	armFromHistory(cfg, ind, m)
	if m.regime != regimeLong {
		t.Fatalf("warm boot should establish LONG, got %d", m.regime)
	}
	if !m.armed || !m.reArmed {
		t.Fatalf("warm boot should arm: armed=%v reArmed=%v", m.armed, m.reArmed)
	}
	if len(sent) != 0 {
		t.Fatalf("warm boot must be silent, got %d alerts", len(sent))
	}

	// First live bar: close 105 lands exactly on the post-push SMA2 = (105+105)/2,
	// the low dips into the band, and it does not breach SMA3 (≈104.67). It should
	// fire exactly once even though the cross was historical.
	live := Bar{BucketStart: msTime(base + 6*180000), Open: 105, High: 105.1, Low: 104.9, Close: 105}
	m.processBar(live)
	if len(sent) != 1 {
		t.Fatalf("first qualifying live bar should fire exactly once, got %d", len(sent))
	}
}

func msTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
