package aggregator

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// closedChan returns an already-ready timer channel so the forward-tracking waits
// resolve instantly in tests.
func closedChan() <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

func TestOutcomeLoggerForwardReturn(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	done := make(chan struct{}, 1)

	o := NewOutcomeLogger(
		func(time.Time) (float64, bool) { return 101.0, true }, // +1% vs baseline 100
		[]int{15, 30},
	)
	o.now = func() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }
	o.after = func(time.Duration) <-chan time.Time { return closedChan() }
	o.emit = func(s string) {
		mu.Lock()
		lines = append(lines, s)
		n := len(lines)
		mu.Unlock()
		if n == 3 { // 1 T0 + 2 forward horizons
			done <- struct{}{}
		}
	}

	o.Record(OutcomeSnapshot{
		T0:            o.now(),
		BaselinePrice: 100,
		ReversalUp:    true, // expect price UP; +1% return is favorable
		Score:         SetupScore{Total: 4, Max: 5, OIDrop: true, CVDNA: true},
		OIChangePct:   -1.2,
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outcome lines")
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], "[OUTCOME-T0]") || !strings.Contains(lines[0], "score=4/5") {
		t.Fatalf("missing/incorrect T0 line: %q", lines[0])
	}
	if !strings.Contains(lines[0], "cvd=na") {
		t.Fatalf("CVD N/A should render as cvd=na: %q", lines[0])
	}
	if !strings.Contains(joined, "h=15m") || !strings.Contains(joined, "h=30m") {
		t.Fatalf("missing forward horizons:\n%s", joined)
	}
	if !strings.Contains(joined, "favorable=true") {
		t.Fatalf("a +1%% move with ReversalUp should be favorable:\n%s", joined)
	}
}

func TestOutcomeFavorableDirection(t *testing.T) {
	// Price fell 1%; for a reversal-DOWN expectation that is favorable.
	var mu sync.Mutex
	var fwd string
	done := make(chan struct{}, 1)

	o := NewOutcomeLogger(func(time.Time) (float64, bool) { return 99.0, true }, []int{15})
	o.now = func() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }
	o.after = func(time.Duration) <-chan time.Time { return closedChan() }
	o.emit = func(s string) {
		if strings.Contains(s, "[OUTCOME-FWD]") {
			mu.Lock()
			fwd = s
			mu.Unlock()
			done <- struct{}{}
		}
	}

	o.Record(OutcomeSnapshot{T0: o.now(), BaselinePrice: 100, ReversalUp: false})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(fwd, "favorable=true") {
		t.Fatalf("a -1%% move with ReversalDown should be favorable: %q", fwd)
	}
}
