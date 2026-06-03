package aggregator

import (
	"sync"
	"time"
)

// flowRetain bounds how long per-bucket flow is kept. The confirmation reads the
// bucket that just closed at the target time (a few minutes out), so a couple of
// hours of retention is ample and keeps the maps tiny.
const flowRetain = 2 * time.Hour

// FlowTracker accumulates signed taker flow into UTC 5-minute buckets for the §6
// confirmation. It feeds two independent signals:
//
//   - perp CVD : net signed taker volume on the perp aggregated-trade streams
//     (+qty for taker-buy, −qty for taker-sell), in quote/USD.
//   - spot net : net spot taker-buy volume (+buy, −sell), in quote/USD.
//
// Values are keyed by bucket-start Unix seconds. The perpSeen/spotSeen flags let
// the confirmation degrade a signal to ⚪ N/A if its stream never delivered any
// data (§6 data-source check), rather than reporting a misleading "fail".
type FlowTracker struct {
	mu       sync.Mutex
	perp     map[int64]float64
	spot     map[int64]float64
	perpSeen bool
	spotSeen bool
}

func NewFlowTracker() *FlowTracker {
	return &FlowTracker{
		perp: make(map[int64]float64),
		spot: make(map[int64]float64),
	}
}

func bucketKeyUnix(ts time.Time) int64 {
	return floorTo5Min(ts).Unix()
}

// AddPerpTrade records a perp taker fill. signedQuoteUSD is +notional for a
// taker-buy and −notional for a taker-sell.
func (f *FlowTracker) AddPerpTrade(ts time.Time, signedQuoteUSD float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.perpSeen = true
	f.perp[bucketKeyUnix(ts)] += signedQuoteUSD
	f.pruneLocked(ts)
}

// AddSpotTrade records a spot taker fill. signedQuoteUSD is +notional for a
// taker-buy and −notional for a taker-sell.
func (f *FlowTracker) AddSpotTrade(ts time.Time, signedQuoteUSD float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spotSeen = true
	f.spot[bucketKeyUnix(ts)] += signedQuoteUSD
	f.pruneLocked(ts)
}

// PerpCVD returns the net signed perp taker volume for the bucket starting at
// bucketStart.
func (f *FlowTracker) PerpCVD(bucketStart time.Time) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perp[bucketKeyUnix(bucketStart)]
}

// SpotNet returns the net spot taker-buy volume for the bucket starting at
// bucketStart.
func (f *FlowTracker) SpotNet(bucketStart time.Time) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spot[bucketKeyUnix(bucketStart)]
}

// PerpActive / SpotActive report whether the corresponding stream has ever
// delivered data. False => the signal renders as ⚪ N/A and is excluded from the
// verdict.
func (f *FlowTracker) PerpActive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perpSeen
}

func (f *FlowTracker) SpotActive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spotSeen
}

// pruneLocked drops buckets older than flowRetain relative to now. Caller holds mu.
func (f *FlowTracker) pruneLocked(now time.Time) {
	cutoff := floorTo5Min(now).Add(-flowRetain).Unix()
	for k := range f.perp {
		if k < cutoff {
			delete(f.perp, k)
		}
	}
	for k := range f.spot {
		if k < cutoff {
			delete(f.spot, k)
		}
	}
}
