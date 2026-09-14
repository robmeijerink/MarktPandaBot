package sweep

import (
	"fmt"
	"log"
	"math"
	"time"
)

// outcomeTracker measures every alert against what price did next, so the module's
// real hit rate can be read from the journal instead of guessed. Joined on id:
//
//   - [SWEEP-OUTCOME-T0]   the alert: entry (candle close), invalidation (wick extreme), risk and features
//   - [SWEEP-OUTCOME-STOP] price traded beyond the wick extreme (the sweep failed), with the best R reached first
//   - [SWEEP-OUTCOME-FWD]  at each horizon: return from entry, best R so far, and whether it was stopped
//
// Candles are checked for the stop before the favourable extreme, so a candle that
// hits both is counted as stopped (conservative).
type outcomeTracker struct {
	horizons []time.Duration
	step     time.Duration
	pending  []*pendingOutcome
	emit     func(string)
}

type pendingOutcome struct {
	id        string
	entryAt   time.Time
	entry     float64
	stop      float64
	long      bool
	mfeR      float64
	stopped   bool
	remaining []time.Duration
}

func newOutcomeTracker(cfg Config) *outcomeTracker {
	o := &outcomeTracker{step: cfg.step(), emit: func(s string) { log.Printf("%s", s) }}
	for _, m := range cfg.OutcomeHorizonsMin {
		if m > 0 {
			o.horizons = append(o.horizons, time.Duration(m)*time.Minute)
		}
	}
	return o
}

// record logs the T0 line and starts tracking the alert from entryAt (the candle close).
func (o *outcomeTracker) record(e sweepEvent, entryAt time.Time) {
	entry := e.bar.Close
	risk := math.Abs(entry - e.extreme)
	id := e.bar.Start.Format("20060102T1504Z")
	o.emit(fmt.Sprintf("[SWEEP-OUTCOME-T0] id=%s dir=%s entry=%.1f stop=%.1f risk=%.3f%% levels=%q wick=%.2fATR wickRatio=%.0f%% vol=%.1fx pen=%.2fATR liq=%.0f liqShare=%.0f%%",
		id, direction(e.low), entry, e.extreme, risk/entry*100, describeLevels(e.levels, e.low),
		e.wickATR, e.wickRatio*100, e.volRatio, e.penATR, e.liqUSD, e.liqShare*100))
	if len(o.horizons) == 0 || risk <= 0 {
		return
	}
	o.pending = append(o.pending, &pendingOutcome{
		id: id, entryAt: entryAt, entry: entry, stop: e.extreme, long: e.low,
		remaining: append([]time.Duration(nil), o.horizons...),
	})
}

// onBar advances every pending alert with a closed candle that started at or after its entry.
func (o *outcomeTracker) onBar(b Bar) {
	kept := o.pending[:0]
	for _, p := range o.pending {
		if b.Start.Before(p.entryAt) {
			kept = append(kept, p)
			continue
		}
		o.advance(p, b)
		if len(p.remaining) > 0 {
			kept = append(kept, p)
		}
	}
	o.pending = kept
}

func (o *outcomeTracker) advance(p *pendingOutcome, b Bar) {
	risk := math.Abs(p.entry - p.stop)
	end := b.Start.Add(o.step)
	if !p.stopped {
		if (p.long && b.Low <= p.stop) || (!p.long && b.High >= p.stop) {
			p.stopped = true
			o.emit(fmt.Sprintf("[SWEEP-OUTCOME-STOP] id=%s after=%dm bestR=%.2f", p.id, int(end.Sub(p.entryAt).Minutes()), p.mfeR))
		} else if p.long {
			p.mfeR = math.Max(p.mfeR, (b.High-p.entry)/risk)
		} else {
			p.mfeR = math.Max(p.mfeR, (p.entry-b.Low)/risk)
		}
	}
	still := p.remaining[:0]
	for _, h := range p.remaining {
		if end.Before(p.entryAt.Add(h)) {
			still = append(still, h)
			continue
		}
		ret := (b.Close - p.entry) / p.entry * 100
		if !p.long {
			ret = -ret
		}
		o.emit(fmt.Sprintf("[SWEEP-OUTCOME-FWD] id=%s h=%dm ret=%+.2f%% bestR=%.2f stopped=%t",
			p.id, int(h.Minutes()), ret, p.mfeR, p.stopped))
	}
	p.remaining = still
}
