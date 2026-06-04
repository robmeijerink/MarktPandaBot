package smaretest

import (
	"math"
	"sync"
	"time"
)

// Bar is one finalized 3m candle. BucketStart is the UTC candle-open time used to
// dedupe the WebSocket (primary) and REST (fallback) feeds.
type Bar struct {
	BucketStart time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
}

// indicators is an in-memory, thread-safe rolling store of the most recent bars
// (capped at capacity). On each finalized bar it can recompute SMA(period) and
// ATR(period). It holds whole bars (not just closes) so ATR can use true range.
type indicators struct {
	mu   sync.Mutex
	bars []Bar // chronological; trimmed to capacity
	cap  int
}

// newIndicators sizes the ring to hold SlowPeriod plus headroom (WarmBootBars).
func newIndicators(cfg Config) *indicators {
	c := cfg.WarmBootBars
	if c < cfg.SlowPeriod {
		c = cfg.SlowPeriod // never store fewer than we need to compute the slow SMA
	}
	return &indicators{cap: c}
}

// push appends a finalized bar and trims to capacity.
func (in *indicators) push(b Bar) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.bars = append(in.bars, b)
	if len(in.bars) > in.cap {
		in.bars = in.bars[len(in.bars)-in.cap:]
	}
}

// lastBucket returns the bucket start of the most recently stored bar.
func (in *indicators) lastBucket() (time.Time, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.bars) == 0 {
		return time.Time{}, false
	}
	return in.bars[len(in.bars)-1].BucketStart, true
}

// fill returns the number of stored bars.
func (in *indicators) fill() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.bars)
}

// ready reports whether enough bars exist to compute the slow SMA (§2: fill >= SlowPeriod).
func (in *indicators) ready(slowPeriod int) bool {
	return in.fill() >= slowPeriod
}

// sma returns the simple moving average of closes over the last `period` bars.
func (in *indicators) sma(period int) (float64, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return smaAt(in.bars, period, len(in.bars)-1)
}

// atr returns the simple average true range over the last `period` bars. True
// range uses the previous bar's close, so it needs period+1 bars.
func (in *indicators) atr(period int) (float64, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if period <= 0 || len(in.bars) <= period {
		return 0, false
	}
	sum := 0.0
	for i := len(in.bars) - period; i < len(in.bars); i++ {
		prevClose := in.bars[i-1].Close
		b := in.bars[i]
		tr := math.Max(b.High-b.Low, math.Max(math.Abs(b.High-prevClose), math.Abs(b.Low-prevClose)))
		sum += tr
	}
	return sum / float64(period), true
}

// smaAt computes the simple moving average of closes ending at endIdx (inclusive)
// over `period` bars. ok is false if there is not enough history.
func smaAt(bars []Bar, period, endIdx int) (float64, bool) {
	if period <= 0 || endIdx < 0 || endIdx-period+1 < 0 {
		return 0, false
	}
	sum := 0.0
	for i := endIdx - period + 1; i <= endIdx; i++ {
		sum += bars[i].Close
	}
	return sum / float64(period), true
}

// barsSinceLastCross scans the stored history and returns how many bars ago the
// fast/slow SMA last changed sign (used to seed the message's "bars since cross"
// after a silent warm boot). If no cross is found inside the ready window it
// returns the number of bars since the indicators first became ready — a safe
// lower bound rather than a fabricated exact value.
func (in *indicators) barsSinceLastCross(fast, slow int) int {
	in.mu.Lock()
	defer in.mu.Unlock()
	n := len(in.bars)
	if n < slow {
		return 0
	}
	lastIdx := n - 1
	crossIdx := slow - 1 // first index where the slow SMA is computable
	prevSign := 0
	for idx := slow - 1; idx <= lastIdx; idx++ {
		f, okF := smaAt(in.bars, fast, idx)
		s, okS := smaAt(in.bars, slow, idx)
		if !okF || !okS {
			continue
		}
		sign := sign2(f - s)
		if prevSign != 0 && sign != prevSign {
			crossIdx = idx
		}
		prevSign = sign
	}
	return lastIdx - crossIdx
}

// sign2 maps a difference to a regime sign: positive => +1 (long), otherwise -1
// (short). Treating exactly-zero as short matches the spec's `curDiff > 0 ? LONG
// : SHORT` and avoids a spurious double cross when the SMAs are momentarily equal.
func sign2(x float64) int {
	if x > 0 {
		return 1
	}
	return -1
}
