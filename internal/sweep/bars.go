package sweep

import (
	"math"
	"sort"
	"time"
)

// Bar is one closed candle. Start is its UTC open time.
type Bar struct {
	Start  time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64 // base asset (BTC)
}

// series keeps the most recent closed bars for the ATR and volume baselines. The
// detector reads it BEFORE pushing the candle under evaluation, so a sweep candle is
// always measured against the candles that came before it.
type series struct {
	bars []Bar
	cap  int
}

func newSeries(cfg Config) *series {
	return &series{cap: max(cfg.ATRPeriod+1, cfg.VolumeLookback)}
}

func (s *series) push(b Bar) {
	s.bars = append(s.bars, b)
	if len(s.bars) > s.cap {
		s.bars = s.bars[len(s.bars)-s.cap:]
	}
}

// atr is the simple average true range of the last `period` bars.
func (s *series) atr(period int) (float64, bool) {
	n := len(s.bars)
	if period <= 0 || n < period+1 {
		return 0, false
	}
	sum := 0.0
	for i := n - period; i < n; i++ {
		prev, b := s.bars[i-1].Close, s.bars[i]
		sum += math.Max(b.High-b.Low, math.Max(math.Abs(b.High-prev), math.Abs(b.Low-prev)))
	}
	return sum / float64(period), true
}

// medianVolume is the median volume of the last n bars (the mean of the middle two
// for an even n).
func (s *series) medianVolume(n int) (float64, bool) {
	if n <= 0 || len(s.bars) < n {
		return 0, false
	}
	vols := make([]float64, n)
	for i, b := range s.bars[len(s.bars)-n:] {
		vols[i] = b.Volume
	}
	sort.Float64s(vols)
	if n%2 == 1 {
		return vols[n/2], true
	}
	return (vols[n/2-1] + vols[n/2]) / 2, true
}
