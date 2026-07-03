package smaretest

import (
	"strings"
	"testing"
	"time"
)

// captureTracker builds a tracker whose lines are captured instead of logged.
func captureTracker(horizonsMin ...int) (*outcomeTracker, *[]string) {
	var lines []string
	o := newOutcomeTracker(horizonsMin)
	o.emit = func(s string) { lines = append(lines, s) }
	return o, &lines
}

// TestOutcomeTrackerForward verifies a fired entry logs a T0 line immediately and an
// FWD line for each horizon once a bar at/after t0+horizon arrives, with the correct
// return and favourability for a long.
func TestOutcomeTrackerForward(t *testing.T) {
	o, lines := captureTracker(15, 30)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	o.record(t0, 100, true, 0.5, 0.2, 7)
	if len(*lines) != 1 || !strings.HasPrefix((*lines)[0], "[SMARETEST-OUTCOME-T0]") {
		t.Fatalf("record should emit exactly one T0 line, got %v", *lines)
	}

	// A bar before the first horizon resolves nothing.
	o.onBar(Bar{BucketStart: t0.Add(10 * time.Minute), Close: 105})
	if len(*lines) != 1 {
		t.Fatalf("no horizon reached yet, should still be 1 line, got %d", len(*lines))
	}

	// A bar at t0+15m resolves the 15m horizon (price up => favorable for a long).
	o.onBar(Bar{BucketStart: t0.Add(15 * time.Minute), Close: 101})
	if len(*lines) != 2 || !strings.Contains((*lines)[1], "h=15m") ||
		!strings.Contains((*lines)[1], "ret=+1.00%") || !strings.Contains((*lines)[1], "favorable=true") {
		t.Fatalf("15m horizon line wrong: %v", *lines)
	}

	// A bar at t0+30m resolves the 30m horizon (price below entry => not favorable).
	o.onBar(Bar{BucketStart: t0.Add(30 * time.Minute), Close: 99})
	if len(*lines) != 3 || !strings.Contains((*lines)[2], "h=30m") || !strings.Contains((*lines)[2], "favorable=false") {
		t.Fatalf("30m horizon line wrong: %v", *lines)
	}

	// All horizons resolved => nothing pending, later bars are inert.
	o.onBar(Bar{BucketStart: t0.Add(60 * time.Minute), Close: 200})
	if len(*lines) != 3 {
		t.Fatalf("all horizons resolved; no further lines expected, got %d", len(*lines))
	}
}

// TestOutcomeTrackerShortAndGap verifies short favourability and that a gapped bar
// past several horizons resolves all of them at once.
func TestOutcomeTrackerShortAndGap(t *testing.T) {
	o, lines := captureTracker(15, 30)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	o.record(t0, 100, false, 0.4, 0.15, 3) // short entry

	// One bar well past both horizons resolves both on the same bar (price down =>
	// favorable for a short).
	o.onBar(Bar{BucketStart: t0.Add(31 * time.Minute), Close: 98})
	fwd := 0
	for _, l := range *lines {
		if strings.HasPrefix(l, "[SMARETEST-OUTCOME-FWD]") {
			fwd++
			if !strings.Contains(l, "favorable=true") {
				t.Fatalf("short with price down should be favorable: %s", l)
			}
		}
	}
	if fwd != 2 {
		t.Fatalf("a bar past both horizons should resolve 2 FWD lines, got %d (%v)", fwd, *lines)
	}
}

// TestOutcomeTrackerDisabled verifies that with no horizons only the T0 line is
// emitted and no state is retained.
func TestOutcomeTrackerDisabled(t *testing.T) {
	o, lines := captureTracker() // no horizons
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	o.record(t0, 100, true, 0.5, 0.2, 7)
	o.onBar(Bar{BucketStart: t0.Add(60 * time.Minute), Close: 110})
	if len(*lines) != 1 {
		t.Fatalf("disabled horizons should emit only the T0 line, got %v", *lines)
	}
}
