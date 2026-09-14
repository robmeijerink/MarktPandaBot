package sweep

import (
	"math"
	"time"
)

// levelKind is where a liquidity level comes from. Each kind is a place resting
// stops and liquidation prices cluster: just beyond an obvious swing, and beyond the
// previous day's and week's extremes.
type levelKind int

const (
	kindHourSwing levelKind = iota
	kindPrevDay
	kindPrevWeek
)

// label names the level for the alert, e.g. "1h swing low" or "previous week high".
func (k levelKind) label(low bool) string {
	side := "high"
	if low {
		side = "low"
	}
	switch k {
	case kindPrevDay:
		return "previous day " + side
	case kindPrevWeek:
		return "previous week " + side
	default:
		return "1h swing " + side
	}
}

// level is one unswept liquidity pool.
type level struct {
	price   float64
	born    time.Time // when the extreme printed (the swing's hour, or the start of the new day/week)
	kind    levelKind
	touches int // equal highs/lows merged into this level (>= 1)
	low     bool
}

// periodExtreme tracks the high and low of the current UTC day or week. complete is
// false when tracking started mid-period (e.g. after a warm boot), so a partial
// extreme is never published as the previous day's/week's level.
type periodExtreme struct {
	start     time.Time
	high, low float64
	complete  bool
	seen      bool
}

// levelBook holds the unswept liquidity levels on both sides and derives new ones
// from the closed bars it is fed. It never looks ahead: a swing is only added once the
// hour that confirms it has closed, and the previous day/week extremes only once that
// period is over.
//
// Per bar the caller runs, in order: open (period rollovers), take (sweep evaluation
// against the levels known before the bar), close (fold the bar in).
type levelBook struct {
	cfg   Config
	step  time.Duration
	lows  []*level
	highs []*level

	hourStart time.Time
	hourBar   Bar
	hourBars  int
	hours     []Bar // completed hourly bars, newest last (kept to 2*SwingStrength+1)

	day, week periodExtreme
}

func newLevelBook(cfg Config) *levelBook {
	return &levelBook{cfg: cfg, step: cfg.step()}
}

// open publishes the previous day's and week's extremes when b starts a new UTC day
// or week.
func (lb *levelBook) open(b Bar) {
	dayStart := b.Start.Truncate(24 * time.Hour)
	lb.roll(&lb.day, dayStart, b, kindPrevDay)
	lb.roll(&lb.week, weekStart(b.Start), b, kindPrevWeek)
}

func (lb *levelBook) roll(p *periodExtreme, start time.Time, b Bar, kind levelKind) {
	if p.seen && start.Equal(p.start) {
		return
	}
	if p.seen && p.complete {
		lb.add(p.low, start, kind, true)
		lb.add(p.high, start, kind, false)
	}
	*p = periodExtreme{start: start, high: b.High, low: b.Low, complete: b.Start.Equal(start), seen: true}
}

// weekStart is the Monday 00:00 UTC of t's week.
func weekStart(t time.Time) time.Time {
	d := t.Truncate(24 * time.Hour)
	offset := (int(d.Weekday()) + 6) % 7 // Monday => 0
	return d.AddDate(0, 0, -offset)
}

// take removes and returns every level on one side that bar b traded through: lows
// under b's low, or highs over b's high. A pierced level is spent whether the bar
// swept it (closed back inside) or broke it (closed beyond) — its stops are gone.
func (lb *levelBook) take(b Bar, low bool) []*level {
	book := &lb.highs
	if low {
		book = &lb.lows
	}
	var pierced, kept []*level
	for _, lv := range *book {
		if (low && b.Low < lv.price) || (!low && b.High > lv.price) {
			pierced = append(pierced, lv)
		} else {
			kept = append(kept, lv)
		}
	}
	*book = kept
	return pierced
}

// close folds a closed bar into the day/week extremes and the hourly swing tracker,
// then expires levels older than MaxLevelAgeHours.
func (lb *levelBook) close(b Bar) {
	for _, p := range []*periodExtreme{&lb.day, &lb.week} {
		p.high = math.Max(p.high, b.High)
		p.low = math.Min(p.low, b.Low)
	}
	lb.foldHour(b)
	cutoff := b.Start.Add(-time.Duration(lb.cfg.MaxLevelAgeHours) * time.Hour)
	lb.lows = unexpired(lb.lows, cutoff)
	lb.highs = unexpired(lb.highs, cutoff)
}

// foldHour builds hourly bars from the timeframe bars; an hour missing any bar is
// discarded rather than published with a partial range.
func (lb *levelBook) foldHour(b Bar) {
	h := b.Start.Truncate(time.Hour)
	if !h.Equal(lb.hourStart) || lb.hourBars == 0 {
		lb.hourStart, lb.hourBar, lb.hourBars = h, Bar{Start: h, Open: b.Open, High: b.High, Low: b.Low}, 0
	}
	lb.hourBar.High = math.Max(lb.hourBar.High, b.High)
	lb.hourBar.Low = math.Min(lb.hourBar.Low, b.Low)
	lb.hourBar.Close = b.Close
	lb.hourBars++
	if lb.hourBars*int(lb.step/time.Minute) < 60 || !b.Start.Add(lb.step).Equal(h.Add(time.Hour)) {
		return
	}
	lb.hourBars = 0
	lb.hours = append(lb.hours, lb.hourBar)
	k := lb.cfg.SwingStrength
	if len(lb.hours) > 2*k+1 {
		lb.hours = lb.hours[len(lb.hours)-(2*k+1):]
	}
	if len(lb.hours) < 2*k+1 {
		return
	}
	// The middle hour is a swing when no hour within k on either side went beyond it.
	mid := lb.hours[k]
	isLow, isHigh := true, true
	for _, x := range lb.hours {
		isLow = isLow && x.Low >= mid.Low
		isHigh = isHigh && x.High <= mid.High
	}
	if isLow {
		lb.add(mid.Low, mid.Start, kindHourSwing, true)
	}
	if isHigh {
		lb.add(mid.High, mid.Start, kindHourSwing, false)
	}
}

// add inserts a level, merging it into an existing level of the same kind within
// EqualLevelTolPct (equal highs/lows): the merged level counts one more touch and
// moves to the more extreme price, where the stops beyond both now sit.
func (lb *levelBook) add(price float64, born time.Time, kind levelKind, low bool) {
	book := &lb.highs
	if low {
		book = &lb.lows
	}
	for _, lv := range *book {
		if lv.kind == kind && math.Abs(lv.price-price)/price*100 <= lb.cfg.EqualLevelTolPct {
			lv.touches++
			if low {
				lv.price = math.Min(lv.price, price)
			} else {
				lv.price = math.Max(lv.price, price)
			}
			return
		}
	}
	*book = append(*book, &level{price: price, born: born, kind: kind, touches: 1, low: low})
}

func unexpired(levels []*level, cutoff time.Time) []*level {
	kept := levels[:0]
	for _, lv := range levels {
		if !lv.born.Before(cutoff) {
			kept = append(kept, lv)
		}
	}
	return kept
}
