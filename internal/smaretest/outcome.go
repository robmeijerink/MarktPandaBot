package smaretest

import (
	"fmt"
	"log"
	"time"
)

// outcomeTracker records, for every FIRED retest alert, the entry feature vector
// (T0) and the realised forward return at several horizons (FWD). None of this
// module's gates are a validated edge — the only honest way to know whether the 1m
// signal actually predicts a move is to label real alerts and measure. It emits two
// grep-friendly stdout lines, joined on id=<entry time>:
//
//   - [SMARETEST-OUTCOME-T0]  one per alert, with the entry context.
//   - [SMARETEST-OUTCOME-FWD] one per (alert, horizon), with the forward return and
//     whether price moved in the entry's favour (up for a long, down for a short).
//
// Unlike the aggregator's OutcomeLogger, forward prices come from the module's own
// bar stream (onBar), not a REST re-fetch — the live feed already delivers every
// closed candle, so a pending outcome resolves when the horizon bar arrives. This
// keeps the module self-contained (isolation rule) and needs no exchange client.
type outcomeTracker struct {
	horizons []time.Duration
	pending  []*pendingOutcome
	emit     func(string) // defaults to log.Printf("%s", line); injectable for tests
}

// pendingOutcome is one fired entry awaiting its forward-return horizons.
type pendingOutcome struct {
	id        string
	t0        time.Time
	entry     float64
	long      bool
	remaining []time.Duration // horizons not yet resolved (ascending)
}

// newOutcomeTracker builds a tracker for the given forward horizons (minutes).
func newOutcomeTracker(horizonsMin []int) *outcomeTracker {
	hs := make([]time.Duration, 0, len(horizonsMin))
	for _, m := range horizonsMin {
		if m > 0 {
			hs = append(hs, time.Duration(m)*time.Minute)
		}
	}
	return &outcomeTracker{horizons: hs, emit: func(s string) { log.Printf("%s", s) }}
}

// record logs the T0 entry line and queues the forward horizons. entry is the touch
// bar's close; t0 is that bar's bucket start (the join key). long marks the regime.
func (o *outcomeTracker) record(t0 time.Time, entry float64, long bool, sepPct, flagRangePct float64, barsSinceCross int) {
	if o == nil {
		return
	}
	dir := "long"
	if !long {
		dir = "short"
	}
	id := t0.Format("20060102T150405Z")
	o.emit(fmt.Sprintf(
		"[SMARETEST-OUTCOME-T0] id=%s dir=%s entry=%.2f sep=%.2f%% flagRange=%.2f%% barsSinceCross=%d",
		id, dir, entry, sepPct, flagRangePct, barsSinceCross))
	if len(o.horizons) == 0 || entry <= 0 {
		return
	}
	rem := make([]time.Duration, len(o.horizons))
	copy(rem, o.horizons)
	o.pending = append(o.pending, &pendingOutcome{id: id, t0: t0, entry: entry, long: long, remaining: rem})
}

// onBar resolves any pending horizons that this bar has reached. A horizon resolves
// on the first bar whose bucket start is at or after t0+horizon, using that bar's
// close as the forward price. Resolved entries are dropped once all horizons are in.
func (o *outcomeTracker) onBar(b Bar) {
	if o == nil || len(o.pending) == 0 {
		return
	}
	kept := o.pending[:0]
	for _, p := range o.pending {
		p.remaining = o.resolveDue(p, b)
		if len(p.remaining) > 0 {
			kept = append(kept, p)
		}
	}
	o.pending = kept
}

// resolveDue emits an FWD line for every horizon of p that b has reached and returns
// the horizons still outstanding.
func (o *outcomeTracker) resolveDue(p *pendingOutcome, b Bar) []time.Duration {
	still := p.remaining[:0]
	for _, h := range p.remaining {
		if b.BucketStart.Before(p.t0.Add(h)) {
			still = append(still, h) // not matured yet; keep waiting
			continue
		}
		ret := (b.Close - p.entry) / p.entry * 100
		favorable := (p.long && ret > 0) || (!p.long && ret < 0)
		o.emit(fmt.Sprintf("[SMARETEST-OUTCOME-FWD] id=%s h=%dm price=%.2f ret=%+.2f%% favorable=%t",
			p.id, int(h.Minutes()), b.Close, ret, favorable))
	}
	return still
}
