package sweep

import (
	"fmt"
	"log"
	"math"
	"time"
)

// sweepEvent is a candle that passed every rule, with the measurements the alert and
// the outcome log report.
type sweepEvent struct {
	bar        Bar
	low        bool     // swept lows (bullish); false = swept highs (bearish)
	levels     []*level // every level the wick took, all reclaimed by the close
	extreme    float64  // the wick's extreme: the invalidation price
	atr        float64
	penATR     float64 // how far the wick cleared the furthest swept level, in ATRs
	wickRatio  float64
	wickATR    float64
	volRatio   float64
	liqUSD     float64 // swept-side liquidations in the candle
	liqVenues  [venueCount]float64
	liqCovered [venueCount]bool
	liqShare   float64
}

// detector evaluates each closed candle against the level book. Bars reach it from a
// single goroutine, so it needs no locking of its own.
type detector struct {
	cfg      Config
	send     func(string)
	liq      *liqStore
	book     *levelBook
	series   *series
	outcomes *outcomeTracker
	lastFire map[bool]time.Time // keyed by event.low
	silent   bool               // warm-boot replay and backfill: update state, send and log nothing
}

func newDetector(cfg Config, liq *liqStore, send func(string)) *detector {
	return &detector{
		cfg:      cfg,
		send:     send,
		liq:      liq,
		book:     newLevelBook(cfg),
		series:   newSeries(cfg),
		outcomes: newOutcomeTracker(cfg),
		lastFire: make(map[bool]time.Time),
	}
}

// processBar runs one closed candle: roll the day/week levels, check both sides for a
// sweep against the levels known before this candle, then fold the candle into the
// levels and baselines.
func (d *detector) processBar(b Bar) {
	d.outcomes.onBar(b)
	d.book.open(b)
	atr, okATR := d.series.atr(d.cfg.ATRPeriod)
	medVol, okVol := d.series.medianVolume(d.cfg.VolumeLookback)
	for _, low := range []bool{true, false} {
		pierced := d.book.take(b, low) // spent either way: swept or broken
		if len(pierced) > 0 && okATR && okVol && atr > 0 && medVol > 0 {
			d.evaluate(b, low, pierced, atr, medVol)
		}
	}
	d.book.close(b)
	d.series.push(b)
}

// evaluate applies the sweep rules to a candle that traded through levels on one
// side, and alerts when all of them hold.
func (d *detector) evaluate(b Bar, low bool, pierced []*level, atr, medVol float64) {
	near, far := pierced[0].price, pierced[0].price // nearest to the close / furthest out
	for _, lv := range pierced {
		if low {
			near, far = math.Max(near, lv.price), math.Min(far, lv.price)
		} else {
			near, far = math.Min(near, lv.price), math.Max(far, lv.price)
		}
	}
	e := sweepEvent{bar: b, low: low, levels: pierced, atr: atr}
	var wick float64
	if low {
		e.extreme, wick = b.Low, math.Min(b.Open, b.Close)-b.Low
	} else {
		e.extreme, wick = b.High, b.High-math.Max(b.Open, b.Close)
	}
	e.penATR = math.Abs(far-e.extreme) / atr
	if rng := b.High - b.Low; rng > 0 {
		e.wickRatio = wick / rng
	}
	e.wickATR = wick / atr
	e.volRatio = b.Volume / medVol

	end := b.Start.Add(d.cfg.step())
	t := d.liq.totals(b.Start, end)
	for v := range venueCount {
		e.liqCovered[v] = d.liq.covered(v, b.Start)
	}
	e.liqUSD, e.liqVenues = t.side(low)
	if all := t.all(); all > 0 {
		e.liqShare = e.liqUSD / all
	}

	c := d.cfg
	var reason string
	switch {
	case (low && b.Close <= near) || (!low && b.Close >= near):
		reason = fmt.Sprintf("closed through %s — a break, not a sweep", describeLevels(pierced, low))
	case e.penATR < c.MinPenetrationATR:
		reason = fmt.Sprintf("barely poked through (%.2f ATR, min %.2f)", e.penATR, c.MinPenetrationATR)
	case e.penATR > c.MaxPenetrationATR:
		reason = fmt.Sprintf("ran %.2f ATR beyond the level (max %.2f) — a breakdown that bounced", e.penATR, c.MaxPenetrationATR)
	case e.wickRatio < c.MinWickRatio:
		reason = fmt.Sprintf("wick is %.0f%% of the candle (min %.0f%%)", e.wickRatio*100, c.MinWickRatio*100)
	case e.wickATR < c.MinWickATR:
		reason = fmt.Sprintf("wick is %.2f ATR (min %.2f)", e.wickATR, c.MinWickATR)
	case e.volRatio < c.MinVolumeRatio:
		reason = fmt.Sprintf("volume %.1fx normal (min %.1fx)", e.volRatio, c.MinVolumeRatio)
	case c.RequireLiquidations && !e.liqCovered[venueBybit]:
		reason = "the Bybit liquidation feed was not connected for the whole candle"
	case c.RequireLiquidations && e.liqUSD < c.MinLiqUSD:
		reason = fmt.Sprintf("%s liquidated %s (min %s)", flushedSide(low), humanUSD(e.liqUSD), humanUSD(c.MinLiqUSD))
	case c.RequireLiquidations && e.liqShare < c.MinLiqSideShare:
		reason = fmt.Sprintf("%s were only %.0f%% of liquidations (min %.0f%%) — a two-sided flush", flushedSide(low), e.liqShare*100, c.MinLiqSideShare*100)
	case d.cooling(low, b.Start):
		reason = fmt.Sprintf("%s cooldown — an alert already went out %.0fm ago (limit 1 per %dm)",
			direction(low), b.Start.Sub(d.lastFire[low]).Minutes(), c.CooldownMin)
	}
	when := b.Start.Format("01-02 15:04")
	if reason != "" {
		d.logf("[SWEEP] %s %s candidate at %s rejected: %s | wick=%.2fATR (%.0f%%) vol=%.1fx pen=%.2fATR liq=%s (%.0f%%)",
			when, direction(low), describeLevels(pierced, low), reason, e.wickATR, e.wickRatio*100, e.volRatio, e.penATR, humanUSD(e.liqUSD), e.liqShare*100)
		return
	}

	d.lastFire[low] = b.Start
	if d.silent {
		return
	}
	msg := buildAlert(c, e)
	if c.LogOnly {
		log.Printf("[SWEEP] %s %s sweep detected (LogOnly, not sent):\n%s", when, direction(low), msg)
	} else {
		log.Printf("[SWEEP] %s %s sweep detected at %s — sending alert.", when, direction(low), describeLevels(pierced, low))
		d.send(msg)
	}
	d.outcomes.record(e, end)
}

// cooling reports whether an alert in this direction went out less than CooldownMin
// before this candle, measured on candle time so replays behave like live.
func (d *detector) cooling(low bool, at time.Time) bool {
	last, ok := d.lastFire[low]
	return d.cfg.CooldownMin > 0 && ok && at.Sub(last) < time.Duration(d.cfg.CooldownMin)*time.Minute
}

func (d *detector) logf(format string, args ...any) {
	if !d.silent {
		log.Printf(format, args...)
	}
}

func direction(low bool) string {
	if low {
		return "BULLISH"
	}
	return "BEARISH"
}

func flushedSide(low bool) string {
	if low {
		return "longs"
	}
	return "shorts"
}
