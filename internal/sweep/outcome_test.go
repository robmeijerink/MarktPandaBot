package sweep

import (
	"strings"
	"testing"
	"time"
)

func captureOutcomes(horizons ...int) (*outcomeTracker, *[]string) {
	cfg := DefaultConfig()
	cfg.OutcomeHorizonsMin = horizons
	o := newOutcomeTracker(cfg)
	var lines []string
	o.emit = func(s string) { lines = append(lines, s) }
	return o, &lines
}

func bullEvent() sweepEvent {
	return sweepEvent{
		bar:     Bar{Start: sweepStart, Open: 99.6, High: 99.8, Low: 98.5, Close: 99.5},
		low:     true,
		levels:  []*level{{price: 99, kind: kindPrevDay, touches: 1, low: true}},
		extreme: 98.5, // risk = 1.0
	}
}

func after(minutes int, o, h, l, c float64) Bar {
	return Bar{Start: sweepStart.Add(time.Duration(minutes) * time.Minute), Open: o, High: h, Low: l, Close: c}
}

// A long that runs 2R, then gives it back, reports the best R reached and the return
// at each horizon; the candle before entry is ignored.
func TestOutcomeTracksBestRAndHorizons(t *testing.T) {
	o, lines := captureOutcomes(15, 30)
	o.record(bullEvent(), sweepStart.Add(5*time.Minute))
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "[SWEEP-OUTCOME-T0]") || !strings.Contains((*lines)[0], "risk=1.005%") {
		t.Fatalf("want one T0 line with the risk, got %v", *lines)
	}
	o.onBar(after(0, 99.6, 105, 90, 99.5)) // the sweep candle itself: before entry, ignored
	o.onBar(after(5, 99.5, 101.5, 99.2, 101))
	o.onBar(after(10, 101, 101.2, 100, 100.5)) // ends at +15m from the sweep open => h=15m not yet (entry +10m)
	o.onBar(after(15, 100.5, 100.6, 99.0, 99.8))
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "h=15m ret=+0.30% bestR=2.00 stopped=false") {
		t.Fatalf("15m horizon should report +0.30%% and 2.00R, got:\n%s", joined)
	}
	o.onBar(after(20, 99.8, 99.9, 99.1, 99.4))
	o.onBar(after(25, 99.4, 99.5, 99.0, 99.2))
	o.onBar(after(30, 99.2, 99.3, 99.0, 99.0))
	joined = strings.Join(*lines, "\n")
	if !strings.Contains(joined, "h=30m ret=-0.50% bestR=2.00 stopped=false") {
		t.Fatalf("30m horizon should report -0.50%%, got:\n%s", joined)
	}
	if len(o.pending) != 0 {
		t.Fatalf("all horizons resolved; nothing should stay pending")
	}
}

// A candle that reaches both the stop and a new high counts as stopped, without
// crediting the high (conservative), and later horizons carry stopped=true.
func TestOutcomeStopIsConservative(t *testing.T) {
	o, lines := captureOutcomes(15)
	o.record(bullEvent(), sweepStart.Add(5*time.Minute))
	o.onBar(after(5, 99.5, 100.0, 99.3, 99.8))  // best 0.5R
	o.onBar(after(10, 99.8, 103.0, 98.4, 99.0)) // both 3.5R and the stop: stopped at 0.5R
	o.onBar(after(15, 99.0, 104.0, 98.9, 103.0))
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "[SWEEP-OUTCOME-STOP] id=20260907T0400Z after=10m bestR=0.50") {
		t.Fatalf("want a STOP line after 10m at 0.50R, got:\n%s", joined)
	}
	if !strings.Contains(joined, "bestR=0.50 stopped=true") {
		t.Fatalf("the horizon after a stop must keep bestR frozen and stopped=true, got:\n%s", joined)
	}
}

// A bearish sweep is measured the other way round: down is favourable.
func TestOutcomeShort(t *testing.T) {
	o, lines := captureOutcomes(15)
	e := sweepEvent{bar: Bar{Start: sweepStart, Close: 100.5}, low: false, extreme: 101.5}
	o.record(e, sweepStart.Add(5*time.Minute))
	o.onBar(after(5, 100.5, 100.8, 99.5, 99.6))
	o.onBar(after(10, 99.6, 99.9, 99.4, 99.5))
	o.onBar(after(15, 99.5, 99.8, 99.45, 99.6)) // closes 15m after entry
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "h=15m ret=+0.90% bestR=1.10 stopped=false") {
		t.Fatalf("a short that fell should report a positive return and 1.10R, got:\n%s", joined)
	}
}
