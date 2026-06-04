package aggregator

import (
	"strings"
	"testing"
	"time"
)

func TestEarlyVerdict(t *testing.T) {
	tests := []struct {
		name     string
		closeOK  bool
		reclaim  bool
		cvd      metricState
		spot     metricState
		watching bool
		want     string
	}{
		{"reclaim + cvd => confirmed", true, true, metricPass, metricFail, false, "Reversal confirmed"},
		{"reclaim + spot => confirmed", true, true, metricFail, metricPass, false, "Reversal confirmed"},
		{"reclaim only, both fail => absorbed", true, true, metricFail, metricFail, false, "Absorbed — weak"},
		{"reclaim only, both NA => absorbed", true, true, metricNA, metricNA, false, "Absorbed — weak"},
		// No reclaim yet but watching => never a premature negative.
		{"no reclaim + watching => pending", true, false, metricPass, metricPass, true, "Pending — watching reclaim"},
		{"no close + watching => pending", false, false, metricNA, metricNA, true, "Pending — watching reclaim"},
		// Watch disabled => the old immediate verdicts.
		{"no reclaim, no watch => not confirmed", true, false, metricPass, metricPass, false, "Not confirmed"},
		{"no close, no watch => inconclusive", false, false, metricPass, metricPass, false, "Inconclusive — no candle close"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := earlyVerdict(tc.closeOK, tc.reclaim, tc.cvd, tc.spot, tc.watching); got != tc.want {
				t.Fatalf("earlyVerdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfirmationReclaimWatchConfirms(t *testing.T) {
	fc := newFakeClock()
	sent := make(chan string, 8)
	cfg := DefaultConfig()
	cfg.ReclaimWatchCandles = 3
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	target := confirmationTarget(now, cfg)
	const flush = 65000.0
	reclaimCandle := target // the candle that closes one interval after target

	cm := NewConfirmationManager(cfg, NewFlowTracker(),
		func(bs time.Time) (float64, bool) {
			if bs.Equal(reclaimCandle) {
				return 70000, true // reclaims above the flush high
			}
			return 60000, true // first candle stays below
		},
		func(msg string) { sent <- msg },
	)
	cm.now = func() time.Time { return now }
	cm.after = fc.after

	cm.Trigger(T0Snapshot{FlushRangeHigh: flush, T0: now})

	// Stage 1 early read: price below flush, so pending + watching.
	(<-fc.calls) <- now
	early := <-sent
	if !strings.Contains(early, "EARLY READ") || !strings.Contains(early, "Pending") {
		t.Fatalf("early read not pending: %q", early)
	}
	if !strings.Contains(early, "Watching up to 3") {
		t.Fatalf("early read missing watch notice: %q", early)
	}

	// Stage 2, first watched candle reclaims.
	(<-fc.calls) <- now
	final := <-sent
	if !strings.Contains(final, "RECLAIM CONFIRMED") {
		t.Fatalf("expected RECLAIM CONFIRMED, got: %q", final)
	}
}

func TestConfirmationReclaimWatchTimesOut(t *testing.T) {
	fc := newFakeClock()
	sent := make(chan string, 8)
	cfg := DefaultConfig()
	cfg.ReclaimWatchCandles = 2
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	cm := NewConfirmationManager(cfg, NewFlowTracker(),
		func(time.Time) (float64, bool) { return 60000, true }, // always below flush
		func(msg string) { sent <- msg },
	)
	cm.now = func() time.Time { return now }
	cm.after = fc.after

	cm.Trigger(T0Snapshot{FlushRangeHigh: 65000, T0: now})

	(<-fc.calls) <- now // stage 1
	<-sent              // early read (pending)
	(<-fc.calls) <- now // watch candle 1 — no reclaim
	(<-fc.calls) <- now // watch candle 2 — no reclaim => final
	final := <-sent
	if !strings.Contains(final, "NOT CONFIRMED") || !strings.Contains(final, "watched 2 candles") {
		t.Fatalf("expected timeout NOT CONFIRMED, got: %q", final)
	}
}

// fakeClock hands out a fresh timer channel per after() call and publishes it so
// the test can detect when a goroutine has reached its select and fire it
// deterministically.
type fakeClock struct {
	calls chan chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{calls: make(chan chan time.Time, 8)} }

func (f *fakeClock) after(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	f.calls <- ch
	return ch
}

func newTestManager(t *testing.T, fc *fakeClock, sent chan string) *ConfirmationManager {
	t.Helper()
	cfg := DefaultConfig()
	cm := NewConfirmationManager(cfg, NewFlowTracker(),
		func(time.Time) (float64, bool) { return 100000, true }, // close above both flush highs => reclaim passes
		func(msg string) { sent <- msg },
	)
	cm.now = func() time.Time { return time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC) }
	cm.after = fc.after
	return cm
}

func TestConfirmationCancellationOnlySecondFires(t *testing.T) {
	fc := newFakeClock()
	sent := make(chan string, 4)
	cm := newTestManager(t, fc, sent)

	cm.Trigger(T0Snapshot{FlushRangeHigh: 1111, T0: cm.now()})
	ch1 := <-fc.calls // goroutine #1 is now at its select

	// A second qualifying T0 cancels the first and starts fresh.
	cm.Trigger(T0Snapshot{FlushRangeHigh: 2222, T0: cm.now()})
	ch2 := <-fc.calls // goroutine #2 is now at its select

	// Fire the second timer; the first must never send even if its timer fires.
	ch2 <- cm.now()
	select {
	case msg := <-sent:
		if !strings.Contains(msg, "2,222") {
			t.Fatalf("fired message is not from the second trigger: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second confirmation never fired")
	}

	// Firing the first (cancelled) timer must produce nothing.
	ch1 <- cm.now()
	select {
	case msg := <-sent:
		t.Fatalf("cancelled first confirmation still sent: %q", msg)
	case <-time.After(200 * time.Millisecond):
		// expected: silence
	}
}

func TestConfirmationReversalConfirmedPath(t *testing.T) {
	fc := newFakeClock()
	sent := make(chan string, 1)
	flow := NewFlowTracker()
	cfg := DefaultConfig()
	cm := NewConfirmationManager(cfg, flow,
		func(time.Time) (float64, bool) { return 100000, true },
		func(msg string) { sent <- msg },
	)
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	cm.now = func() time.Time { return now }
	cm.after = fc.after

	// Seed positive perp CVD in the bucket that will be evaluated. With now on a
	// boundary, target = now+300s and the evaluated candle opens at `now`.
	target := confirmationTarget(now, cfg)
	candleStart := target.Add(-time.Duration(cfg.CandleIntervalSec) * time.Second)
	flow.AddPerpTrade(candleStart.Add(30*time.Second), 5000) // taker-buy notional

	cm.Trigger(T0Snapshot{FlushRangeHigh: 99000, T0: now})
	ch := <-fc.calls
	ch <- now

	select {
	case msg := <-sent:
		if !strings.Contains(msg, "Reversal confirmed") {
			t.Fatalf("verdict not confirmed: %q", msg)
		}
		if !strings.Contains(msg, "✅ CONFIRMATION") {
			t.Fatalf("confirmed message missing success header: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmation never fired")
	}
}
