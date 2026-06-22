package smaretest

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// Run is the module entry point: it warm-boots the regime silently, then consumes
// finalized 3m bars from the WebSocket (primary) and a REST poll (fallback),
// running the state machine on a single goroutine. It is meant to be launched as
// `go smaretest.Run(cfg, send)` and blocks forever. `send` delivers a finished
// alert string (wire it to the existing Telegram client in main).
func Run(cfg Config, send func(string)) {
	log.Printf("[SMARETEST] Starting 21/%d SMA retest module (%s %s, dirs=%s).",
		cfg.SlowPeriod, cfg.Symbol, cfg.Timeframe, cfg.Directions)

	ind := newIndicators(cfg)
	m := newMachine(cfg, ind, send)
	client := &http.Client{Timeout: time.Duration(cfg.KlineFetchTimeoutSec) * time.Second}

	tracker := &bucketTracker{}
	warmBoot(cfg, ind, m, tracker, client)

	barCh := make(chan Bar, 16)
	go runKlineWS(cfg, barCh)                    // primary source
	go restFallback(cfg, client, barCh, tracker) // fallback when WS is silent

	// Single consumer: serializes all state transitions (§concurrency). Dedupe by
	// bucket so the WS and REST feeds never double-count the same bar.
	for b := range barCh {
		if !tracker.update(b.BucketStart) {
			continue // already processed (WS+REST overlap) or out-of-order
		}
		m.processBar(b)
	}
}

// warmBoot fetches WarmBootBars closed bars and establishes the initial regime
// SILENTLY (§3): no alert is emitted for the historical cross. On failure it
// retries with linear backoff; if it still fails it starts "not ready" and lets
// live bars fill the window. It never blocks indefinitely and never crashes.
func warmBoot(cfg Config, ind *indicators, m *machine, tracker *bucketTracker, client *http.Client) {
	var bars []Bar
	for attempt := 1; attempt <= cfg.KlineMaxRetries; attempt++ {
		got, err := warmBootFetch(client, cfg)
		if err == nil && len(got) > 0 {
			bars = got
			break
		}
		if err != nil {
			log.Printf("[SMARETEST] Warm boot attempt %d/%d failed: %v", attempt, cfg.KlineMaxRetries, err)
		}
		time.Sleep(time.Duration(attempt) * time.Second) // linear backoff
	}
	if len(bars) == 0 {
		log.Println("[SMARETEST] Warm boot: no historical klines; starting not-ready (live bars will fill the window).")
		return
	}

	for _, b := range bars {
		ind.push(b)
	}
	if last, ok := ind.lastBucket(); ok {
		tracker.update(last) // live bars at/<= this bucket are skipped as duplicates
	}

	if !ind.ready(cfg.SlowPeriod) {
		log.Printf("[SMARETEST] Warm boot hydrated %d bars (< SlowPeriod %d); waiting for more before arming.",
			ind.fill(), cfg.SlowPeriod)
		return
	}

	armFromHistory(cfg, ind, m)
	regimeName := "LONG (golden cross)"
	if m.regime == regimeShort {
		regimeName = "SHORT (death cross)"
	}
	log.Printf("[SMARETEST] Warm boot complete: %d bars, regime=%s, ~%d bars since cross. Armed silently (no alert).",
		ind.fill(), regimeName, m.barsSinceCross)
}

// armFromHistory sets the initial regime SILENTLY from already-hydrated indicators
// (§3 step 3): regime = sign(SMA21 - SMA200), state = ARMED, reArmed = true. It
// emits no alert; touches may fire on the first qualifying live bar even though the
// cross is historical. Indicators must already be ready.
func armFromHistory(cfg Config, ind *indicators, m *machine) {
	fast, _ := ind.sma(cfg.FastPeriod)
	slow, _ := ind.sma(cfg.SlowPeriod)
	if fast > slow {
		m.regime = regimeLong
	} else {
		m.regime = regimeShort
	}
	m.armed = true
	m.reArmed = true // first qualifying live bar may fire even though the cross is historical
	m.prevFast, m.prevSlow, m.havePrev = fast, slow, true
	m.barsSinceCross = ind.barsSinceLastCross(cfg.FastPeriod, cfg.SlowPeriod)
	// Seed the move-away peak from history so a setup that already separated from the
	// 21 SMA before startup can still fire on the first qualifying live retest.
	m.maxSep = ind.maxSeparationSinceCross(cfg.FastPeriod, cfg.SlowPeriod)
}

// restFallback polls one closed bar per timeframe boundary, but ONLY when the
// WebSocket has not already delivered that bucket — making REST a true backstop.
// It applies BarCloseGraceSec so it reads the settled candle, not an in-progress
// one. It blocks forever and is meant to run in its own goroutine.
func restFallback(cfg Config, client *http.Client, barCh chan<- Bar, tracker *bucketTracker) {
	step := intervalDuration(cfg.Timeframe)
	grace := time.Duration(cfg.BarCloseGraceSec) * time.Second
	log.Printf("[SMARETEST] REST fallback armed (poll per %s boundary + %ds grace, only if WS is silent).",
		cfg.Timeframe, cfg.BarCloseGraceSec)

	for {
		now := time.Now().UTC()
		nextBoundary := now.Truncate(step).Add(step)
		time.Sleep(nextBoundary.Add(grace).Sub(now))

		justClosed := nextBoundary.Add(-step)
		if !tracker.get().Before(justClosed) {
			continue // WS already delivered this bucket (primary path won)
		}
		bars, err := fetchClosedBars(client, cfg, 0, 3)
		if err != nil {
			log.Printf("[SMARETEST] REST fallback fetch error for bucket %s: %v", justClosed.Format("15:04"), err)
			continue
		}
		for _, b := range bars {
			if b.BucketStart.Equal(justClosed) {
				log.Printf("[SMARETEST] REST fallback supplying bucket %s (WS missed it).", justClosed.Format("15:04"))
				barCh <- b
				break
			}
		}
	}
}

// bucketTracker records the most recently accepted bar bucket so the WS (primary)
// and REST (fallback) feeds can be de-duplicated and the fallback can tell whether
// the WS already covered a bucket. Guarded by its own mutex.
type bucketTracker struct {
	mu   sync.Mutex
	last time.Time
}

// update records b if it is newer than the last accepted bucket, returning true
// when it is new (the caller should process it) and false for duplicates/old bars.
func (t *bucketTracker) update(b time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !b.After(t.last) {
		return false
	}
	t.last = b
	return true
}

// get returns the most recently accepted bucket.
func (t *bucketTracker) get() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}
