package bybit

import (
	"errors"
	"log"
	"sync"
	"time"
)

// Bybit REST is geo-blocked (HTTP 403) on some hosts even though the WebSocket
// feed keeps working (see the bybit-rest-georestricted note). Rather than retry a
// hard block on every 5-minute poll — spamming logs and wasting two requests per
// cycle across both hosts — this small circuit breaker pauses REST kline fetches
// after a run of consecutive failures and retries occasionally in case the block
// lifts. It only gates REST; nothing here touches the WebSocket streams.
const (
	bybitBreakerThreshold = 5                // consecutive failures before pausing
	bybitBreakerCooldown  = 15 * time.Minute // pause/retry interval while open
)

// ErrRESTPaused is returned by FetchKlines while the breaker is open. Callers can
// detect it with errors.Is to stay quiet — the breaker already logged the cause.
var ErrRESTPaused = errors.New("bybit: REST kline fetches paused (circuit open)")

type restBreaker struct {
	mu      sync.Mutex
	fails   int
	open    bool
	nextTry time.Time
	now     func() time.Time // injectable for tests
}

var bybitREST = &restBreaker{now: time.Now}

// allow reports whether a real request should be attempted now. While open it
// blocks until the cooldown elapses, then permits a single half-open trial.
func (b *restBreaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.open || !b.now().Before(b.nextTry)
}

// onSuccess closes the breaker and clears the failure run.
func (b *restBreaker) onSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		log.Println("[BYBIT] REST recovered; resuming kline fetches.")
	}
	b.fails = 0
	b.open = false
}

// onFailure records a failed fetch, tripping the breaker once the threshold is
// reached (logged once) and extending the cooldown on a failed half-open trial.
func (b *restBreaker) onFailure(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails++
	switch {
	case b.open:
		b.nextTry = b.now().Add(bybitBreakerCooldown) // half-open trial failed; wait again
	case b.fails >= bybitBreakerThreshold:
		b.open = true
		b.nextTry = b.now().Add(bybitBreakerCooldown)
		log.Printf("[BYBIT] REST failed %d× consecutively (%v); pausing kline fetches for %s "+
			"(likely geo-block; WebSocket feeds unaffected).", b.fails, err, bybitBreakerCooldown)
	}
}

func (b *restBreaker) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails = 0
	b.open = false
	b.nextTry = time.Time{}
}
