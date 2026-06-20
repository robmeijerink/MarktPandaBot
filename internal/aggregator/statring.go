package aggregator

import "sync"

// StatRing is a thread-safe fixed-length FIFO ring of float64 samples used to turn
// fixed thresholds into adaptive, distribution-relative ones. The engine feeds one
// sample per evaluation cycle (every cycle, including idle ones, so the trailing
// distribution is representative) and reads percentile ranks / lookback values when
// scoring a setup. It is deliberately tiny and independent of VolumeRing so the OI
// and funding histories can have their own sizes/semantics.
type StatRing struct {
	mu    sync.RWMutex
	data  []float64
	size  int
	fill  int // number of real samples held (<= size)
	next  int // index of the next write
	count int // total samples ever added (for warm-up gating)
}

// NewStatRing builds a ring holding up to size samples. size <= 0 is coerced to 1.
func NewStatRing(size int) *StatRing {
	if size <= 0 {
		size = 1
	}
	return &StatRing{data: make([]float64, size), size: size}
}

// Add appends v, dropping the oldest once at capacity. O(1).
func (r *StatRing) Add(v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[r.next] = v
	r.next = (r.next + 1) % r.size
	if r.fill < r.size {
		r.fill++
	}
	r.count++
}

// Fill reports how many real samples are currently held.
func (r *StatRing) Fill() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fill
}

// Rank returns the fraction of held samples strictly less than v (0..1), the
// percentile of v within the trailing distribution. ok is false until at least
// minSamples are held, so a cold ring falls back to the caller's fixed bar instead
// of ranking against two or three points.
func (r *StatRing) Rank(v float64, minSamples int) (rank float64, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fill < minSamples || r.fill == 0 {
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

// Ago returns the sample n positions before the most recent one (Ago(1) == the
// previous sample), with ok=false if that many samples are not held yet. Used by
// the funding-trend test to read funding ~FundingLookbackHours ago.
func (r *StatRing) Ago(n int) (float64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n < 0 || n >= r.fill {
		return 0, false
	}
	idx := (r.next - 1 - n + r.size*2) % r.size
	return r.data[idx], true
}
