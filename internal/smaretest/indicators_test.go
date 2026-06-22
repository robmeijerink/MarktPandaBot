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
	cfg.FlagMaxRangePct = 0.5
	cfg.FlagContractionRatio = 0.8

	base := int64(1_700_000_000_000)
	pushOHLC := func(in *indicators, bars ...Bar) {
		for i, b := range bars {
			b.BucketStart = time.UnixMilli(base + int64(i)*180000).UTC()
			in.push(b)
		}
	}

	// Contracting range: wide earlier half, tight recent half, then a touch candidate
	// bar appended last (excluded from the flag window).
	in := &indicators{cap: 100}
	pushOHLC(in,
		bar(100, 100.6, 99.4, 100), bar(100, 100.5, 99.5, 100), // earlier half: ~1.2% range
		bar(100, 100.6, 99.5, 100), bar(100, 100.5, 99.4, 100),
		bar(100, 100.10, 99.95, 100), bar(100, 100.08, 99.96, 100), // recent half: ~0.15% range
		bar(100, 100.09, 99.95, 100), bar(100, 100.07, 99.96, 100),
		bar(100, 100.02, 99.90, 100), // touch candidate (excluded)
	)
	fw := in.flag(cfg.FlagLookback)
	if !fw.ok {
		t.Fatalf("flag window should be ready with enough bars")
	}
	if !tightFlag(cfg, fw) {
		t.Fatalf("contracting tight range should qualify: recent=%.3f earlier=%.3f", fw.recentRange, fw.earlierRange)
	}

	// Non-contracting (wide throughout) => not a tight range.
	in2 := &indicators{cap: 100}
	pushOHLC(in2,
		bar(100, 100.6, 99.4, 100), bar(100, 100.5, 99.5, 100),
		bar(100, 100.6, 99.5, 100), bar(100, 100.5, 99.4, 100),
		bar(100, 100.6, 99.4, 100), bar(100, 100.5, 99.5, 100),
		bar(100, 100.6, 99.5, 100), bar(100, 100.5, 99.4, 100),
		bar(100, 100.02, 99.90, 100),
	)
	if tightFlag(cfg, in2.flag(cfg.FlagLookback)) {
		t.Fatalf("a wide, non-contracting window must not qualify as a tight range")
	}

	// Not enough history => window not ok, predicate false.
	in3 := &indicators{cap: 100}
	pushOHLC(in3, bar(100, 100.1, 99.9, 100), bar(100, 100.1, 99.9, 100))
	if fw3 := in3.flag(cfg.FlagLookback); fw3.ok || tightFlag(cfg, fw3) {
		t.Fatalf("flag must be not-ok / false without enough history")
	}
}

// TestMaxSeparationSinceCross verifies the warm-boot seed: the peak excursion of
// price from the 21 SMA, in the trend direction, since the most recent cross.
func TestMaxSeparationSinceCross(t *testing.T) {
	in := &indicators{cap: 100}
	// fast=2, slow=3. Dip then a strong rally so SMA2 crosses above SMA3 (LONG), with
	// price running well above the fast SMA after the cross.
	pushCloses(in, 10, 9, 8, 7, 6, 8, 11, 15)
	sep := in.maxSeparationSinceCross(2, 3)
	if sep <= 0 {
		t.Fatalf("a long leg above the 21 SMA should report positive separation, got %.4f", sep)
	}
	// Flat series => no separation.
	in2 := &indicators{cap: 100}
	pushCloses(in2, 100, 100, 100, 100, 100)
	if s := in2.maxSeparationSinceCross(2, 3); s != 0 {
		t.Fatalf("a flat series has no separation, got %.4f", s)
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
