// Package volume hydrates and maintains the aggregated (Bybit + OKX) 5-minute
// quote-volume ring buffer used by the Vol Spike check. It is the one place that
// imports both exchange packages so the warm-boot and live paths can be summed
// identically (D5/§1): the same closed-kline quote-volume definition on both.
//
// All time math is UTC (D6); the host clock must be NTP-synced.
package volume

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
	"github.com/robmeijerink/MarktPandaBot/internal/bybit"
	"github.com/robmeijerink/MarktPandaBot/internal/okx"
)

// klineFetcher abstracts each exchange's REST kline fetch so warm boot and the
// live poll treat both venues uniformly.
type klineFetcher struct {
	name  string
	fetch func(client *http.Client, limit int) ([]aggregator.Kline, error)
}

func fetchers() []klineFetcher {
	return []klineFetcher{
		{name: "bybit", fetch: bybit.FetchKlines},
		{name: "okx", fetch: okx.FetchKlines},
	}
}

func httpClient(cfg aggregator.Config) *http.Client {
	return &http.Client{Timeout: time.Duration(cfg.KlineFetchTimeoutSec) * time.Second}
}

// WarmBoot hydrates the ring from REST BEFORE any WebSocket is opened (§3). It
// fetches BufferSize closed klines per exchange, sums quote-volume per UTC bucket
// across exchanges, and pushes the most recent BufferSize buckets chronologically.
// It never blocks indefinitely and never crashes on failure: a failed leg simply
// contributes nothing and the MinBufferFill gate keeps Vol Spike at 0 until live
// buckets accumulate (§4).
func WarmBoot(ring *aggregator.VolumeRing, cfg aggregator.Config) {
	client := httpClient(cfg)
	sums := make(map[int64]float64)

	for _, f := range fetchers() {
		klines := fetchWithRetry(client, f, cfg)
		if len(klines) == 0 {
			log.Printf("[VOLUME] Warm boot: %s returned no klines; continuing with partial/empty buffer.", f.name)
			continue
		}
		for _, k := range klines {
			sums[k.BucketStart.Unix()] += k.QuoteVol
		}
		log.Printf("[VOLUME] Warm boot: %s hydrated %d closed buckets.", f.name, len(klines))
	}

	if len(sums) == 0 {
		log.Println("[VOLUME] Warm boot: no historical klines hydrated; starting cold (warming up).")
		return
	}

	keys := make([]int64, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if len(keys) > cfg.BufferSize {
		keys = keys[len(keys)-cfg.BufferSize:] // keep the most recent BufferSize
	}
	for _, k := range keys {
		ring.Add(sums[k])
	}
	log.Printf("[VOLUME] Warm boot complete: ring fill=%d/%d (MinBufferFill %d).",
		ring.Fill(), cfg.BufferSize, cfg.MinBufferFill)
}

// RunLivePoll appends one aggregated bucket per UTC 5-minute boundary, using the
// same closed-kline quote-volume as warm boot so the median stays meaningful. It
// blocks forever and is meant to run in its own goroutine.
func RunLivePoll(ring *aggregator.VolumeRing, cfg aggregator.Config) {
	const settleDelay = 5 * time.Second // let the just-closed candle finalise on the venue
	client := httpClient(cfg)

	log.Println("[VOLUME] Live volume poll started (per UTC 5-min boundary).")
	for {
		now := time.Now().UTC()
		nextBoundary := now.Truncate(5 * time.Minute).Add(5 * time.Minute)
		time.Sleep(nextBoundary.Add(settleDelay).Sub(now))

		bucket := nextBoundary.Add(-5 * time.Minute) // the bucket that just closed
		sum, matched := aggregatedBucketVol(client, cfg, bucket)
		if matched == 0 {
			log.Printf("[VOLUME] Live poll: no closed kline found for bucket %s; skipping this cycle.",
				bucket.Format("15:04"))
			continue
		}
		ring.Add(sum)
		log.Printf("[VOLUME] Live poll: bucket %s aggregated %.0f quote-vol from %d venue(s); ring fill=%d.",
			bucket.Format("15:04"), sum, matched, ring.Fill())
	}
}

// aggregatedBucketVol sums the quote-volume of the closed kline that opened at
// `bucket` across exchanges. It returns the sum and how many venues supplied it.
func aggregatedBucketVol(client *http.Client, cfg aggregator.Config, bucket time.Time) (sum float64, matched int) {
	for _, f := range fetchers() {
		klines, err := f.fetch(client, 3)
		if err != nil {
			log.Printf("[VOLUME] Live poll: %s fetch error: %v", f.name, err)
			continue
		}
		for _, k := range klines {
			if k.BucketStart.Equal(bucket) {
				sum += k.QuoteVol
				matched++
				break
			}
		}
	}
	return sum, matched
}

// fetchWithRetry retries a kline fetch up to KlineMaxRetries with linear backoff.
// On total failure it returns nil (best-effort warm boot, never fatal).
func fetchWithRetry(client *http.Client, f klineFetcher, cfg aggregator.Config) []aggregator.Kline {
	var lastErr error
	for attempt := 1; attempt <= cfg.KlineMaxRetries; attempt++ {
		klines, err := f.fetch(client, cfg.BufferSize)
		if err == nil {
			return klines
		}
		lastErr = err
		log.Printf("[VOLUME] Warm boot: %s attempt %d/%d failed: %v", f.name, attempt, cfg.KlineMaxRetries, err)
		time.Sleep(time.Duration(attempt) * time.Second) // linear backoff
	}
	log.Printf("[VOLUME] Warm boot: %s gave up after %d attempts: %v", f.name, cfg.KlineMaxRetries, lastErr)
	return nil
}
