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
