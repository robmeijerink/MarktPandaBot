package sweep

import (
	"testing"
	"time"
)

// monday is a Monday 00:00 UTC, so day and week rollovers are easy to reason about.
var monday = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)

func levelConfig() Config {
	cfg := DefaultConfig()
	cfg.Timeframe = "5m"
	cfg.SwingStrength = 2
	cfg.EqualLevelTolPct = 0.05
	cfg.MaxLevelAgeHours = 168
	return cfg
}

// hourOf returns the twelve 5m bars of one hour whose range is [low, high].
func hourOf(start time.Time, low, high float64) []Bar {
	bars := make([]Bar, 12)
	mid := (low + high) / 2
	for i := range bars {
		bars[i] = Bar{Start: start.Add(time.Duration(i) * 5 * time.Minute), Open: mid, High: mid, Low: mid, Close: mid, Volume: 1}
	}
	bars[5].Low = low
	bars[7].High = high
	return bars
}

// feedBook runs bars through the per-bar order the detector uses.
func feedBook(lb *levelBook, bars []Bar) {
	for _, b := range bars {
		lb.open(b)
		lb.take(b, true)
		lb.take(b, false)
		lb.close(b)
	}
}

func prices(levels []*level) []float64 {
	out := make([]float64, len(levels))
	for i, lv := range levels {
		out[i] = lv.price
	}
	return out
}

// A 1h swing only becomes a level once SwingStrength further hours have closed, never
// earlier (no lookahead).
func TestHourSwingConfirmedWithoutLookahead(t *testing.T) {
	lb := newLevelBook(levelConfig())
	lows := []float64{100.5, 100.2, 99.0, 100.1, 100.4}
	var bars []Bar
	for i, lo := range lows {
		bars = append(bars, hourOf(monday.Add(time.Duration(i)*time.Hour), lo, lo+1.5)...)
	}
	// Everything but the final bar of the fifth hour: the swing at hour 3 is unconfirmed.
	feedBook(lb, bars[:len(bars)-1])
	for _, lv := range lb.lows {
		if lv.kind == kindHourSwing {
			t.Fatalf("swing low published before its confirming hour closed: %v", prices(lb.lows))
		}
	}
	feedBook(lb, bars[len(bars)-1:])
	var swing *level
	for _, lv := range lb.lows {
		if lv.kind == kindHourSwing {
			swing = lv
		}
	}
	if swing == nil || swing.price != 99.0 || !swing.born.Equal(monday.Add(2*time.Hour)) {
		t.Fatalf("want a 1h swing low at 99.0 born at hour 2, got %+v", swing)
	}
}

// A bar that trades through a level spends it; a bar that stays above keeps it.
func TestTakeSpendsPiercedLevels(t *testing.T) {
	lb := newLevelBook(levelConfig())
	lb.add(99.0, monday, kindHourSwing, true)
	lb.add(98.0, monday, kindHourSwing, true)
	if got := lb.take(Bar{Start: monday.Add(time.Hour), High: 100, Low: 98.5, Close: 99.5}, true); len(got) != 1 || got[0].price != 99.0 {
		t.Fatalf("a low at 98.5 should spend only the 99.0 level, got %v", prices(got))
	}
	if len(lb.lows) != 1 || lb.lows[0].price != 98.0 {
		t.Fatalf("the untouched 98.0 level must remain, got %v", prices(lb.lows))
	}
	if got := lb.take(Bar{Start: monday.Add(time.Hour), High: 100, Low: 98.0, Close: 99.5}, true); len(got) != 0 {
		t.Fatalf("a low exactly at the level has not traded through it, got %v", prices(got))
	}
}

// The previous day's extremes are published at the next UTC day's first bar, but
// only for a day observed from its start; a partial day (e.g. after a mid-day boot)
// is never published.
func TestPreviousDayLevelsNeedACompleteDay(t *testing.T) {
	flat := func(start time.Time, n int, lo, hi float64) []Bar {
		bars := make([]Bar, n)
		for i := range bars {
			bars[i] = Bar{Start: start.Add(time.Duration(i) * 5 * time.Minute), Open: 100, High: hi, Low: lo, Close: 100}
		}
		return bars
	}

	// Complete Monday, then Tuesday's first bar.
	lb := newLevelBook(levelConfig())
	feedBook(lb, flat(monday, 288, 95, 105))
	feedBook(lb, flat(monday.Add(24*time.Hour), 1, 99, 101))
	var pdl, pdh bool
	for _, lv := range lb.lows {
		pdl = pdl || (lv.kind == kindPrevDay && lv.price == 95)
	}
	for _, lv := range lb.highs {
		pdh = pdh || (lv.kind == kindPrevDay && lv.price == 105)
	}
	if !pdl || !pdh {
		t.Fatalf("a complete day must publish PDL 95 and PDH 105: lows=%v highs=%v", prices(lb.lows), prices(lb.highs))
	}

	// Monday observed only from noon: nothing is published on Tuesday.
	lb2 := newLevelBook(levelConfig())
	feedBook(lb2, flat(monday.Add(12*time.Hour), 144, 95, 105))
	feedBook(lb2, flat(monday.Add(24*time.Hour), 1, 99, 101))
	for _, lv := range append(lb2.lows, lb2.highs...) {
		if lv.kind == kindPrevDay {
			t.Fatalf("a partially observed day must not publish a level, got %+v", lv)
		}
	}
}

// Swing lows within EqualLevelTolPct merge into one level with more touches, at the
// lower price where the stops below both now sit.
func TestEqualLowsMerge(t *testing.T) {
	lb := newLevelBook(levelConfig())
	lb.add(100.00, monday, kindHourSwing, true)
	lb.add(99.97, monday.Add(3*time.Hour), kindHourSwing, true) // 0.03% away
	lb.add(99.50, monday.Add(5*time.Hour), kindHourSwing, true) // 0.5% away: separate
	if len(lb.lows) != 2 || lb.lows[0].touches != 2 || lb.lows[0].price != 99.97 {
		t.Fatalf("want one double-touch level at 99.97 plus 99.50, got %+v %+v", *lb.lows[0], lb.lows[1:])
	}
	lb.add(99.99, monday, kindPrevDay, true) // a different kind never merges into a swing
	if len(lb.lows) != 3 {
		t.Fatalf("levels of different kinds must stay separate, got %v", prices(lb.lows))
	}
}

// Levels older than MaxLevelAgeHours are dropped.
func TestLevelsExpire(t *testing.T) {
	cfg := levelConfig()
	cfg.MaxLevelAgeHours = 24
	lb := newLevelBook(cfg)
	lb.add(99.0, monday, kindHourSwing, true)
	lb.close(Bar{Start: monday.Add(24 * time.Hour), Open: 100, High: 100, Low: 100, Close: 100})
	if len(lb.lows) != 1 {
		t.Fatalf("a level exactly MaxLevelAgeHours old is still valid, got %v", prices(lb.lows))
	}
	lb.close(Bar{Start: monday.Add(24*time.Hour + 5*time.Minute), Open: 100, High: 100, Low: 100, Close: 100})
	if len(lb.lows) != 0 {
		t.Fatalf("a level older than MaxLevelAgeHours must expire, got %v", prices(lb.lows))
	}
}

func TestWeekStartsMonday(t *testing.T) {
	sunday := monday.Add(6*24*time.Hour + 23*time.Hour)
	if got := weekStart(sunday); !got.Equal(monday) {
		t.Fatalf("Sunday 23:00 belongs to the week starting %v, got %v", monday, got)
	}
	if got := weekStart(monday.Add(7 * 24 * time.Hour)); !got.Equal(monday.Add(7 * 24 * time.Hour)) {
		t.Fatalf("the next Monday starts a new week, got %v", got)
	}
}
