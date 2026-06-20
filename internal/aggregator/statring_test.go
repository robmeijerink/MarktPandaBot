package aggregator

import "testing"

func TestStatRingRank(t *testing.T) {
	r := NewStatRing(10)
	for _, v := range []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		r.Add(v)
	}
	// 9 of 10 samples are below 9.5 => rank 0.9.
	rank, ok := r.Rank(9.5, 5)
	if !ok || rank != 0.9 {
		t.Fatalf("Rank(9.5) = %.2f ok=%v, want 0.90", rank, ok)
	}
	// Below the warm-up min => not ok.
	r2 := NewStatRing(10)
	r2.Add(1)
	r2.Add(2)
	if _, ok := r2.Rank(1.5, 5); ok {
		t.Fatal("Rank should be not-ok before minSamples")
	}
}

func TestStatRingAgo(t *testing.T) {
	r := NewStatRing(3) // capacity 3: oldest is dropped
	r.Add(10)
	r.Add(20)
	r.Add(30)
	if v, ok := r.Ago(0); !ok || v != 30 {
		t.Fatalf("Ago(0) = %.0f ok=%v, want 30 (most recent)", v, ok)
	}
	if v, ok := r.Ago(2); !ok || v != 10 {
		t.Fatalf("Ago(2) = %.0f ok=%v, want 10", v, ok)
	}
	r.Add(40) // evicts 10; ring is now 20,30,40
	if v, ok := r.Ago(2); !ok || v != 20 {
		t.Fatalf("after eviction Ago(2) = %.0f ok=%v, want 20", v, ok)
	}
	if _, ok := r.Ago(3); ok {
		t.Fatal("Ago(3) should be not-ok with only 3 samples")
	}
}

func TestVolumeRingRank(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BufferSize = 10
	cfg.MinBufferFill = 5
	r := NewVolumeRing(cfg)
	for _, v := range []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100} {
		r.Add(v)
	}
	rank, ok := r.Rank(95)
	if !ok || rank != 0.9 {
		t.Fatalf("Rank(95) = %.2f ok=%v, want 0.90", rank, ok)
	}

	// Warming ring: below minFill => not ok.
	r2 := NewVolumeRing(cfg)
	r2.Add(10)
	if _, ok := r2.Rank(10); ok {
		t.Fatal("Rank should be not-ok while the ring is warming")
	}
}
