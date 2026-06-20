package aggregator

import (
	"sort"
	"sync"
	"time"
)

// Kline is one closed 5-minute candle, used by both the warm-boot hydration and
// the live volume poll. QuoteVol is the closed candle's quote/USD volume — the
// SAME definition on both paths (D5/§1), so the median stays meaningful. Close is
// used by the §6 reclaim check on the primary exchange.
type Kline struct {
	BucketStart time.Time // UTC 5-minute boundary (candle open time)
	Open        float64
	High        float64
	Low         float64
	Close       float64
	QuoteVol    float64
}

// VolumeRing is a thread-safe fixed-length FIFO ring of COMPLETED 5-minute
// aggregated (Bybit + OKX) quote-volume buckets. The in-progress bucket is never
// stored; it is Add()ed only once it closes. Reads happen on T0 evaluation,
// writes on bucket rollover — guarded by its own RWMutex (never share this lock
// with the confirmation cancelFunc mutex, per §7).
type VolumeRing struct {
	mu      sync.RWMutex
	data    []float64 // len == size, used as a circular buffer
	size    int
	fill    int // number of real samples currently held (<= size)
	next    int // index of the next write
	minFill int // Median returns ok=false while fill < minFill
}

// NewVolumeRing builds a ring sized from cfg.BufferSize with the cfg.MinBufferFill
// gate.
func NewVolumeRing(cfg Config) *VolumeRing {
	size := cfg.BufferSize
	if size <= 0 {
		size = 1
	}
	return &VolumeRing{
		data:    make([]float64, size),
		size:    size,
		minFill: cfg.MinBufferFill,
	}
}

// Add appends a completed bucket's aggregated quote-volume, dropping the oldest
// once at capacity. O(1).
func (r *VolumeRing) Add(v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[r.next] = v
	r.next = (r.next + 1) % r.size
	if r.fill < r.size {
		r.fill++
	}
}

// Median returns the median of the held samples. ok is false while
// fill < minFill (still warming up) — in that case Vol Spike scores 0 and the
// matrix shows "(warming up)".
func (r *VolumeRing) Median() (float64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fill < r.minFill || r.fill == 0 {
		return 0, false
	}
	vals := make([]float64, r.fill)
	if r.fill < r.size {
		copy(vals, r.data[:r.fill])
	} else {
		copy(vals, r.data)
	}
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2], true
	}
	return (vals[n/2-1] + vals[n/2]) / 2, true
}

// Rank returns the fraction of held samples strictly less than v (0..1) — the
// percentile of v within the ring. ok is false while fill < minFill (still
// warming), so the adaptive Vol Spike gate falls back to the fixed multiplier.
func (r *VolumeRing) Rank(v float64) (float64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fill < r.minFill || r.fill == 0 {
		return 0, false
	}
	below := 0
	for i := 0; i < r.fill; i++ {
		if r.data[i] < v {
			below++
		}
	}
	return float64(below) / float64(r.fill), true
}

// Latest returns the most recently added (most recent completed) bucket volume.
// This is the "current bucket volume" compared against Median() at T0. ok is false
// when the ring is empty.
func (r *VolumeRing) Latest() (float64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fill == 0 {
		return 0, false
	}
	idx := (r.next - 1 + r.size) % r.size
	return r.data[idx], true
}

// Fill reports how many real samples are currently held.
func (r *VolumeRing) Fill() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fill
}
