package bybit

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBybitRESTCircuitBreaker(t *testing.T) {
	bybitREST.reset()
	clock := time.Now()
	bybitREST.now = func() time.Time { return clock }
	defer func() {
		bybitREST.reset()
		bybitREST.now = time.Now
	}()

	var hits int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer blocked.Close()

	old := bybitRESTBases
	bybitRESTBases = []string{blocked.URL}
	defer func() { bybitRESTBases = old }()

	// Drive the breaker to its threshold; each call really hits the server.
	for i := 0; i < bybitBreakerThreshold; i++ {
		if _, err := FetchKlines(blocked.Client(), 5); err == nil {
			t.Fatalf("call %d: expected failure", i)
		}
	}
	hitsAtTrip := atomic.LoadInt32(&hits)

	// Now open: further calls short-circuit to ErrRESTPaused without any HTTP.
	if _, err := FetchKlines(blocked.Client(), 5); !errors.Is(err, ErrRESTPaused) {
		t.Fatalf("want ErrRESTPaused while open, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != hitsAtTrip {
		t.Fatalf("breaker open but server was hit again (%d -> %d)", hitsAtTrip, got)
	}

	// Before cooldown elapses it stays paused.
	clock = clock.Add(bybitBreakerCooldown - time.Second)
	if _, err := FetchKlines(blocked.Client(), 5); !errors.Is(err, ErrRESTPaused) {
		t.Fatalf("want still-paused before cooldown, got %v", err)
	}

	// After cooldown it half-opens and retries; point it at a healthy server so
	// the trial succeeds and the breaker closes.
	cur := time.Now().UTC().Truncate(5 * time.Minute)
	closed := cur.Add(-5 * time.Minute)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"retCode":0,"result":{"list":[%s,%s]}}`,
			bybitKlineRow(cur, 7, 7, 7, 7, 1, 1),
			bybitKlineRow(closed, 2, 3, 1, 2.5, 10, 2500))
	}))
	defer good.Close()
	bybitRESTBases = []string{good.URL}
	clock = clock.Add(2 * time.Second) // now past nextTry

	if _, err := FetchKlines(good.Client(), 5); err != nil {
		t.Fatalf("expected half-open trial to succeed, got %v", err)
	}
	// Closed again: subsequent calls go straight through.
	if _, err := FetchKlines(good.Client(), 5); err != nil {
		t.Fatalf("expected breaker closed after recovery, got %v", err)
	}
}
