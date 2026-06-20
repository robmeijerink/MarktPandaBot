package smaretest

import (
	"math"
	"testing"
	"time"
)

func pushCloses(in *indicators, closes ...float64) {
	base := int64(1_700_000_000_000)
	for i, c := range closes {
		in.push(Bar{BucketStart: time.UnixMilli(base + int64(i)*180000).UTC(), Open: c, High: c, Low: c, Close: c})
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSMA(t *testing.T) {
	in := &indicators{cap: 100}
	pushCloses(in, 1, 2, 3, 4, 5)

	if v, ok := in.sma(5); !ok || !approx(v, 3) {
		t.Fatalf("sma(5) = %v ok=%v, want 3", v, ok)
	}
	if v, ok := in.sma(2); !ok || !approx(v, 4.5) {
		t.Fatalf("sma(2) = %v ok=%v, want 4.5", v, ok)
	}
	if _, ok := in.sma(6); ok {
		t.Fatalf("sma(6) should be not-ok with only 5 bars")
	}
}

func TestATR(t *testing.T) {
	in := &indicators{cap: 100}
	// Bars with a constant true range of 2: each high-low = 2 and closes flat.
	base := int64(1_700_000_000_000)
	for i := 0; i < 5; i++ {
		c := 100.0
		in.push(Bar{BucketStart: time.UnixMilli(base + int64(i)*180000).UTC(), Open: c, High: c + 1, Low: c - 1, Close: c})
	}
	if v, ok := in.atr(3); !ok || !approx(v, 2) {
		t.Fatalf("atr(3) = %v ok=%v, want 2", v, ok)
	}
}

func TestReadyGate(t *testing.T) {
	in := &indicators{cap: 100}
	pushCloses(in, 1, 2)
	if in.ready(3) {
		t.Fatalf("should not be ready with 2 < 3 bars")
	}
	pushCloses(in, 3)
	if !in.ready(3) {
		t.Fatalf("should be ready with 3 >= 3 bars")
	}
}

func TestFlagTight(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FlagLookback = 8
	cfg.PoleLookback = 4
	cfg.FlagMaxRangePct = 0.5
	cfg.FlagContractionRatio = 0.8

	base := int64(1_700_000_000_000)
	pushOHLC := func(in *indicators, bars ...Bar) {
		for i, b := range bars {
			b.BucketStart = time.UnixMilli(base + int64(i)*180000).UTC()
			in.push(b)
		}
	}
	// 4 pole-history bars so flag() has room for the pole window; their values do not
	// matter to tightFlag (it only reads the flag window).
	poleHist := []Bar{bar(100, 100.1, 99.9, 100), bar(100, 100.1, 99.9, 100), bar(100, 100.1, 99.9, 100), bar(100, 100.1, 99.9, 100)}

	// Contracting flag: wide earlier half, tight recent half, then a touch candidate
	// bar appended last (excluded from the flag window).
	in := &indicators{cap: 100}
	pushOHLC(in, append(append([]Bar{}, poleHist...),
		bar(100, 100.6, 99.4, 100), bar(100, 100.5, 99.5, 100), // earlier half: ~1.2% range
		bar(100, 100.6, 99.5, 100), bar(100, 100.5, 99.4, 100),
		bar(100, 100.10, 99.95, 100), bar(100, 100.08, 99.96, 100), // recent half: ~0.15% range
		bar(100, 100.09, 99.95, 100), bar(100, 100.07, 99.96, 100),
		bar(100, 100.02, 99.90, 100), // touch candidate (excluded)
	)...)
	fw := in.flag(cfg.FlagLookback, cfg.PoleLookback)
	if !fw.ok {
		t.Fatalf("flag window should be ready with enough bars")
	}
	if !tightFlag(cfg, fw) {
		t.Fatalf("contracting tight flag should qualify: recent=%.3f earlier=%.3f", fw.recentRange, fw.earlierRange)
	}

	// Non-contracting (wide throughout) => not a tight flag.
	in2 := &indicators{cap: 100}
	pushOHLC(in2, append(append([]Bar{}, poleHist...),
		bar(100, 100.6, 99.4, 100), bar(100, 100.5, 99.5, 100),
		bar(100, 100.6, 99.5, 100), bar(100, 100.5, 99.4, 100),
		bar(100, 100.6, 99.4, 100), bar(100, 100.5, 99.5, 100),
		bar(100, 100.6, 99.5, 100), bar(100, 100.5, 99.4, 100),
		bar(100, 100.02, 99.90, 100),
	)...)
	if tightFlag(cfg, in2.flag(cfg.FlagLookback, cfg.PoleLookback)) {
		t.Fatalf("a wide, non-contracting window must not qualify as a tight flag")
	}

	// Not enough history => window not ok, predicate false.
	in3 := &indicators{cap: 100}
	pushOHLC(in3, bar(100, 100.1, 99.9, 100), bar(100, 100.1, 99.9, 100))
	if fw3 := in3.flag(cfg.FlagLookback, cfg.PoleLookback); fw3.ok || tightFlag(cfg, fw3) {
		t.Fatalf("flag must be not-ok / false without enough history")
	}
}

// TestFlagPole verifies the pole gate: a tight flag only counts as a model entry
// when a real, trend-aligned impulse precedes it. Case 2 reproduces the live false
// trigger — a geometrically tight window with no pole must now be rejected.
func TestFlagPole(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FlagLookback = 8
	cfg.PoleLookback = 4
	cfg.FlagMaxRangePct = 0.5
	cfg.FlagContractionRatio = 0.8
	cfg.FlagMinPolePct = 0.6
	cfg.FlagMinPoleRatio = 1.5

	base := int64(1_700_000_000_000)
	build := func(bars ...Bar) *indicators {
		in := &indicators{cap: 100}
		for i, b := range bars {
			b.BucketStart = time.UnixMilli(base + int64(i)*180000).UTC()
			in.push(b)
		}
		return in
	}
	// One tight, contracting flag shape reused for every case: earlier half ~0.4
	// range, recent half ~0.15, fullRange ~0.4. Only the PRECEDING bars differ.
	flagBars := []Bar{
		bar(100, 100.1, 99.7, 100), bar(100, 100.1, 99.7, 100),
		bar(100, 100.1, 99.7, 100), bar(100, 100.1, 99.7, 100),
		bar(100, 100.05, 99.9, 100), bar(100, 100.05, 99.9, 100),
		bar(100, 100.05, 99.9, 100), bar(100, 100.05, 99.9, 100),
	}
	touch := bar(100, 100.02, 99.90, 100)
	with := func(pre []Bar) flagWindow {
		in := build(append(append(append([]Bar{}, pre...), flagBars...), touch)...)
		return in.flag(cfg.FlagLookback, cfg.PoleLookback)
	}

	// Case 1: a real UP pole (99.0 -> 100.0, +1.0%) => valid for LONG, not SHORT.
	up := with([]Bar{bar(99, 99, 99, 99), bar(99.3, 99.3, 99.3, 99.3), bar(99.6, 99.6, 99.6, 99.6), bar(100, 100, 100, 100)})
	if !tightFlag(cfg, up) {
		t.Fatalf("setup: flag should read as tight; recent=%.3f earlier=%.3f", up.recentRange, up.earlierRange)
	}
	if !validPole(cfg, up, regimeLong) {
		t.Fatalf("a real up-pole before a tight flag should validate for LONG: poleMove=%.3f full=%.3f", up.poleMove, up.fullRange)
	}
	if validPole(cfg, up, regimeShort) {
		t.Fatalf("an up-pole must NOT validate a SHORT (wrong direction)")
	}

	// Case 2: flat chop, NO pole — the live false trigger. The flag is still tight,
	// but with no pole it must be rejected for BOTH directions.
	flat := with([]Bar{bar(100, 100.05, 99.95, 100), bar(100, 100.05, 99.95, 100), bar(100, 100.05, 99.95, 100), bar(100, 100.05, 99.95, 100)})
	if !tightFlag(cfg, flat) {
		t.Fatalf("setup: the chop flag is geometrically tight (that is the trap)")
	}
	if validPole(cfg, flat, regimeLong) || validPole(cfg, flat, regimeShort) {
		t.Fatalf("flat chop has no pole and must be rejected: poleMove=%.4f", flat.poleMove)
	}

	// Case 3: a weak drift below the impulse floor (+0.3%% < 0.6%%) is not a pole.
	weak := with([]Bar{bar(99.7, 99.7, 99.7, 99.7), bar(99.8, 99.8, 99.8, 99.8), bar(99.9, 99.9, 99.9, 99.9), bar(100, 100, 100, 100)})
	if validPole(cfg, weak, regimeLong) {
		t.Fatalf("a sub-threshold drift is not a pole: poleMove=%.3f", weak.poleMove)
	}

	// Case 4: a real DOWN pole (101.0 -> 100.0, -1.0%) => valid for SHORT, not LONG.
	down := with([]Bar{bar(101, 101, 101, 101), bar(100.7, 100.7, 100.7, 100.7), bar(100.4, 100.4, 100.4, 100.4), bar(100, 100, 100, 100)})
	if !validPole(cfg, down, regimeShort) {
		t.Fatalf("a real down-pole should validate for SHORT: poleMove=%.3f", down.poleMove)
	}
	if validPole(cfg, down, regimeLong) {
		t.Fatalf("a down-pole must NOT validate a LONG")
	}
}

func TestBarsSinceLastCross(t *testing.T) {
	in := &indicators{cap: 100}
	// fast=2, slow=3. Build a series that crosses up near the end.
	// Down then up so SMA2 dips below SMA3 then rises above it.
	pushCloses(in, 10, 9, 8, 7, 6, 7, 9, 11)
	got := in.barsSinceLastCross(2, 3)
	if got < 0 || got > len(in.bars)-1 {
		t.Fatalf("barsSinceLastCross out of range: %d", got)
	}
	// The most recent cross is a golden cross (rising tail), so the final regime is
	// long and the count should be small (a few bars), not the whole window.
	if got >= 6 {
		t.Fatalf("expected a recent cross (small count), got %d", got)
	}
}
