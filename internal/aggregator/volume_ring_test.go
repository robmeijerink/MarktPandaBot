package aggregator

import (
	"testing"
	"time"
)

func TestVolumeRingFIFODropAtCapacity(t *testing.T) {
	cfg := Config{BufferSize: 3, MinBufferFill: 1}
	r := NewVolumeRing(cfg)
	r.Add(1)
	r.Add(2)
	r.Add(3)
	if r.Fill() != 3 {
		t.Fatalf("Fill = %d, want 3", r.Fill())
	}
	// Overflow: oldest (1) drops, contents become {2,3,4}.
	r.Add(4)
	if r.Fill() != 3 {
		t.Fatalf("Fill after overflow = %d, want 3 (capped)", r.Fill())
	}
	if got, _ := r.Latest(); got != 4 {
		t.Fatalf("Latest = %v, want 4", got)
	}
	// Median of {2,3,4} = 3, confirming 1 was dropped.
	if m, ok := r.Median(); !ok || m != 3 {
		t.Fatalf("Median = %v ok=%v, want 3 true", m, ok)
	}
}

func TestVolumeRingMedianCorrectness(t *testing.T) {
	cfg := Config{BufferSize: 10, MinBufferFill: 1}

	// Odd count: median is the middle element.
	r := NewVolumeRing(cfg)
	for _, v := range []float64{5, 1, 3} {
		r.Add(v)
	}
	if m, ok := r.Median(); !ok || m != 3 {
		t.Fatalf("odd median = %v ok=%v, want 3 true", m, ok)
	}

	// Even count: median is the mean of the two middle elements.
	r2 := NewVolumeRing(cfg)
	for _, v := range []float64{1, 2, 3, 4} {
		r2.Add(v)
	}
	if m, ok := r2.Median(); !ok || m != 2.5 {
		t.Fatalf("even median = %v ok=%v, want 2.5 true", m, ok)
	}
}

func TestVolumeRingNotReadyBelowMinFill(t *testing.T) {
	cfg := Config{BufferSize: 10, MinBufferFill: 3}
	r := NewVolumeRing(cfg)
	r.Add(10)
	r.Add(20)
	if _, ok := r.Median(); ok {
		t.Fatalf("Median ok=true below MinBufferFill, want false")
	}
	r.Add(30) // now fill == MinBufferFill
	if m, ok := r.Median(); !ok || m != 20 {
		t.Fatalf("Median = %v ok=%v at MinBufferFill, want 20 true", m, ok)
	}
}

func TestConfirmationTargetEffectiveWaitBounds(t *testing.T) {
	cfg := DefaultConfig() // MinLeadSeconds 180, CandleIntervalSec 300
	lo := time.Duration(cfg.MinLeadSeconds) * time.Second
	hi := time.Duration(cfg.MinLeadSeconds+cfg.CandleIntervalSec) * time.Second // 480s

	base := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC) // on a 5-min boundary
	for offset := 0; offset < 300; offset++ {
		now := base.Add(time.Duration(offset) * time.Second)
		target := confirmationTarget(now, cfg)
		wait := target.Sub(now)
		if wait < lo || wait >= hi {
			t.Fatalf("offset %ds: effective wait %s out of [%s, %s)", offset, wait, lo, hi)
		}
		// Target must be a real 5-minute boundary and strictly in the future.
		if !target.Equal(floorTo5Min(target)) {
			t.Fatalf("offset %ds: target %s is not on a 5-min boundary", offset, target)
		}
		if !target.After(now) {
			t.Fatalf("offset %ds: target %s not after now %s", offset, target, now)
		}
	}
}

func TestConfirmationTargetMinLeadBoundary(t *testing.T) {
	cfg := DefaultConfig()
	base := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	// Exactly MinLeadSeconds (180s) of lead: 180 is NOT < 180, so target the
	// imminent close, wait == 180s.
	now := base.Add(time.Duration(cfg.CandleIntervalSec-cfg.MinLeadSeconds) * time.Second) // 120s in
	if got := confirmationTarget(now, cfg).Sub(now); got != 180*time.Second {
		t.Fatalf("at exactly MinLead boundary wait = %s, want 180s", got)
	}

	// One second less lead (179s): skip to the candle after, wait == 179+300.
	now2 := now.Add(time.Second) // 121s in => 179s lead
	if got := confirmationTarget(now2, cfg).Sub(now2); got != 479*time.Second {
		t.Fatalf("just under MinLead wait = %s, want 479s", got)
	}
}
