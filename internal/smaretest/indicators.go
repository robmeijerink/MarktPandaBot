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

// flagWindow describes the consolidation ("range") leading up to the most recent
// bar. It is the price range over a lookback window, split into an earlier and a
// recent half, so the state machine can tell whether volatility is contracting
// into a tight range before a 21 SMA touch — the CryptoLifer "model" entry premise.
type flagWindow struct {
	fullRange    float64 // high-low over the whole window
	earlierRange float64 // high-low over the older half
	recentRange  float64 // high-low over the newer half
	refPrice     float64 // last close in the window, for percent math
	ok           bool    // false when there is not enough history for the window
}

// flag measures the consolidation over the `lookback` bars ending JUST BEFORE the
// most recent bar. The last stored bar is the touch candidate; the range is the
// run-up that precedes it, so it is excluded here. ok is false until enough bars
// exist (warm boot fills the window long before this matters).
func (in *indicators) flag(lookback int) flagWindow {
	in.mu.Lock()
	defer in.mu.Unlock()
	n := len(in.bars)
	end := n - 2 // bar before the touch candidate
	start := end - lookback + 1
	if lookback < 2 || start < 0 {
		return flagWindow{}
	}
	half := lookback / 2
	rng := func(lo, hi int) float64 {
		mn, mx := in.bars[lo].Low, in.bars[lo].High
		for i := lo + 1; i <= hi; i++ {
			mn = math.Min(mn, in.bars[i].Low)
			mx = math.Max(mx, in.bars[i].High)
		}
		return mx - mn
	}
	return flagWindow{
		fullRange:    rng(start, end),
		earlierRange: rng(start, start+half-1),
		recentRange:  rng(end-half+1, end),
		refPrice:     in.bars[end].Close,
		ok:           true,
	}
}

// tightFlag reports whether the range qualifies as a tight, contracting consolidation
// per the config: the recent half must be tight right now (height <= FlagMaxRangePct
// of price) AND getting tighter (recent half <= FlagContractionRatio of the earlier
// half). A degenerate flat earlier half counts as maximally contracted.
func tightFlag(cfg Config, fw flagWindow) bool {
	if !fw.ok || fw.refPrice <= 0 {
		return false
	}
	if fw.recentRange/fw.refPrice*100 > cfg.FlagMaxRangePct {
		return false // not tight enough yet
	}
	if fw.earlierRange <= 0 {
		return fw.recentRange <= 0 // both flat => maximally tight
	}
	return fw.recentRange <= cfg.FlagContractionRatio*fw.earlierRange
}

// maxSeparationSinceCross returns how far price moved AWAY from the fast (21) SMA,
// in the trend direction, at its furthest point since the most recent fast/slow
// cross — as a fraction of price. For a long it is the largest (High-fast)/fast; for
// a short the largest (fast-Low)/fast. It mirrors barsSinceLastCross: it scans the
// ready window, finds the last sign flip of (fast-slow), then measures the peak
// excursion from that bar onward. Used to seed the live state after a silent warm
// boot so a setup that already separated before startup can still fire.
func (in *indicators) maxSeparationSinceCross(fast, slow int) float64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	n := len(in.bars)
	if n < slow {
		return 0
	}
	lastIdx := n - 1
	crossIdx := slow - 1
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
	regime := prevSign // sign of (fast-slow) at the most recent bar
	maxSep := 0.0
	for idx := crossIdx; idx <= lastIdx; idx++ {
		f, ok := smaAt(in.bars, fast, idx)
		if !ok || f <= 0 {
			continue
		}
		var sep float64
		if regime == regimeLong {
			sep = (in.bars[idx].High - f) / f
		} else {
			sep = (f - in.bars[idx].Low) / f
		}
		if sep > maxSep {
			maxSep = sep
		}
	}
	return maxSep
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
