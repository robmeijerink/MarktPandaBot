package aggregator

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// metricState is the tri-state of a confirmation signal: a pass, a fail, or N/A
// (the underlying stream was unavailable, §6 data-source check).
type metricState int

const (
	metricFail metricState = iota
	metricPass
	metricNA
)

// confirmSettleDelay is how long past the candle close we wait before fetching it,
// giving the REST feed time to publish the closed candle. confirmCloseRetries is a
// safety net if it is still momentarily unavailable.
const (
	confirmSettleDelay  = 10 * time.Second
	confirmCloseRetries = 3
	confirmRetryWait    = 2 * time.Second
)

func (m metricState) glyph() string {
	switch m {
	case metricPass:
		return "✅"
	case metricNA:
		return "⚪"
	default:
		return "❌"
	}
}

// CloseFunc returns the closing price of the 5-minute candle starting at
// bucketStart on the PrimaryExchange (ok=false if it could not be fetched). It is
// injected so the aggregator package needs no dependency on the exchange packages
// and so it can be faked in tests.
type CloseFunc func(bucketStart time.Time) (price float64, ok bool)

// SendFunc dispatches the independent confirmation message (Telegram in prod,
// a capture in tests).
type SendFunc func(msg string)

// T0Snapshot is the state captured into the confirmation goroutine at trigger
// time so a later live update cannot mutate what we confirm against.
type T0Snapshot struct {
	FlushRangeHigh float64   // liquidation range top on PrimaryExchange
	BaselinePrice  float64   // PrimaryExchange last price at T0
	T0             time.Time // UTC timestamp of the triggering alert
}

// ConfirmationManager owns the single in-flight candle-sync confirmation. A new
// qualifying T0 cancels any pending confirmation and starts a fresh one (§6.1).
// The cancelFunc is guarded by its own mutex, kept separate from the ring's lock
// (§7).
type ConfirmationManager struct {
	cfg        Config
	flow       *FlowTracker
	fetchClose CloseFunc
	send       SendFunc

	// Injectable clock — real time in prod, controllable in tests.
	now   func() time.Time
	after func(d time.Duration) <-chan time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	gen    int // generation of the active confirmation, for safe cancel-clearing
}

func NewConfirmationManager(cfg Config, flow *FlowTracker, fetchClose CloseFunc, send SendFunc) *ConfirmationManager {
	return &ConfirmationManager{
		cfg:        cfg,
		flow:       flow,
		fetchClose: fetchClose,
		send:       send,
		now:        func() time.Time { return time.Now().UTC() },
		after:      time.After,
	}
}

// Trigger cancels any running confirmation and launches a fresh one for snap.
// Caller must already have checked the D3 gate.
func (c *ConfirmationManager) Trigger(snap T0Snapshot) {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel() // cancel the previous, still-pending confirmation
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.gen++
	myGen := c.gen
	c.cancel = cancel
	c.mu.Unlock()

	go c.run(ctx, snap, myGen)
}

// run drives the two-stage confirmation: an early flow read at the first closed
// candle, then — only if price has not reclaimed yet — a watch over the next few
// candles for a reclaim. Every wait is cancelable; a newer T0 ends the whole run
// silently (§7).
func (c *ConfirmationManager) run(ctx context.Context, snap T0Snapshot, myGen int) {
	defer c.clearCancel(myGen)

	target := confirmationTarget(c.now(), c.cfg)
	interval := time.Duration(c.cfg.CandleIntervalSec) * time.Second

	// ── Stage 1: early flow read at the first closed candle. The settle delay
	// lets the exchange publish the just-closed candle before we fetch it. ──
	if !c.waitUntil(ctx, target.Add(confirmSettleDelay)) {
		return
	}
	candleStart := target.Add(-interval)
	closePx, ok := c.fetchCloseWithRetry(candleStart)
	reclaim := ok && closePx > snap.FlushRangeHigh
	cvd, spot := c.evalFlow(candleStart)

	watching := !reclaim && c.cfg.ReclaimWatchCandles > 0
	c.send(formatConfirmation(confirmationView{
		minutesWaited:  int(math.Round(target.Sub(snap.T0).Minutes())),
		candleStart:    candleStart,
		closePx:        closePx,
		closeOK:        ok,
		flushRangeHigh: snap.FlushRangeHigh,
		reclaim:        reclaim,
		cvd:            cvd,
		spot:           spot,
		verdict:        earlyVerdict(ok, reclaim, cvd, spot, watching),
		watching:       watching,
		watchCandles:   c.cfg.ReclaimWatchCandles,
	}))
	if !watching {
		return // price already reclaimed, or the watch is disabled
	}

	// ── Stage 2: watch subsequent candles for a price reclaim. ──
	var lastClose float64
	var lastOK bool
	for i := 1; i <= c.cfg.ReclaimWatchCandles; i++ {
		candleClose := target.Add(time.Duration(i) * interval)
		if !c.waitUntil(ctx, candleClose.Add(confirmSettleDelay)) {
			return
		}
		cs := candleClose.Add(-interval)
		lastClose, lastOK = c.fetchCloseWithRetry(cs)
		if lastOK && lastClose > snap.FlushRangeHigh {
			c.send(formatReclaimResult(reclaimResult{
				reclaimed:      true,
				minutesWaited:  int(math.Round(candleClose.Sub(snap.T0).Minutes())),
				candleStart:    cs,
				closePx:        lastClose,
				closeOK:        true,
				flushRangeHigh: snap.FlushRangeHigh,
				candlesWatched: i,
			}))
			return
		}
	}
	// Watched the full window with no reclaim.
	finalClose := target.Add(time.Duration(c.cfg.ReclaimWatchCandles) * interval)
	c.send(formatReclaimResult(reclaimResult{
		reclaimed:      false,
		minutesWaited:  int(math.Round(finalClose.Sub(snap.T0).Minutes())),
		closePx:        lastClose,
		closeOK:        lastOK,
		flushRangeHigh: snap.FlushRangeHigh,
		candlesWatched: c.cfg.ReclaimWatchCandles,
	}))
}

// waitUntil blocks until time t (per the injectable clock) or cancellation. It
// returns false if the run was cancelled.
func (c *ConfirmationManager) waitUntil(ctx context.Context, t time.Time) bool {
	wait := t.Sub(c.now())
	if wait < 0 {
		wait = 0
	}
	select {
	case <-ctx.Done():
		return false
	case <-c.after(wait):
		return true
	}
}

// clearCancel drops the stored cancelFunc on natural completion, but only if a
// newer Trigger has not already replaced it.
func (c *ConfirmationManager) clearCancel(myGen int) {
	c.mu.Lock()
	if c.gen == myGen {
		c.cancel = nil
	}
	c.mu.Unlock()
}

// evalFlow reads the two optional flow signals for the given candle.
func (c *ConfirmationManager) evalFlow(candleStart time.Time) (cvd, spot metricState) {
	cvd, spot = metricNA, metricNA
	if c.flow.PerpActive() {
		if c.flow.PerpCVD(candleStart) > 0 {
			cvd = metricPass
		} else {
			cvd = metricFail
		}
	}
	if c.flow.SpotActive() {
		if c.flow.SpotNet(candleStart) > 0 {
			spot = metricPass
		} else {
			spot = metricFail
		}
	}
	return cvd, spot
}

// earlyVerdict is the verdict on the FIRST candle (the early flow read):
//   - Reclaim passes AND (CVD OR Spot passes)    => "Reversal confirmed" (final)
//   - Reclaim passes, both flow signals fail/NA  => "Absorbed — weak" (final)
//   - Reclaim not yet passed, watch enabled      => "Pending — watching reclaim"
//   - Reclaim not passed, no watch, no close     => "Inconclusive — no candle close"
//   - Reclaim not passed, no watch, close present => "Not confirmed"
//
// When watching, we never say "Not confirmed" prematurely — the final negative is
// only issued by the stage-2 watch after the price has had several candles to
// reclaim. This is what prevents a slow reversal from being mislabelled early.
func earlyVerdict(closeOK, reclaim bool, cvd, spot metricState, watching bool) string {
	if reclaim {
		if cvd == metricPass || spot == metricPass {
			return "Reversal confirmed"
		}
		return "Absorbed — weak"
	}
	if watching {
		return "Pending — watching reclaim"
	}
	if !closeOK {
		return "Inconclusive — no candle close"
	}
	return "Not confirmed"
}

// fetchCloseWithRetry retries the close fetch a few times; the just-closed candle
// can take a moment to appear on the REST feed even after the settle delay.
func (c *ConfirmationManager) fetchCloseWithRetry(bucketStart time.Time) (float64, bool) {
	for attempt := 0; attempt < confirmCloseRetries; attempt++ {
		if px, ok := c.fetchClose(bucketStart); ok {
			return px, true
		}
		if attempt < confirmCloseRetries-1 {
			time.Sleep(confirmRetryWait)
		}
	}
	return 0, false
}

type confirmationView struct {
	minutesWaited  int
	candleStart    time.Time
	closePx        float64
	closeOK        bool
	flushRangeHigh float64
	reclaim        bool
	cvd            metricState
	spot           metricState
	verdict        string
	watching       bool // a stage-2 reclaim watch will follow this early read
	watchCandles   int
}

// formatConfirmation renders the independent confirmation message (§7). The lead
// glyph reflects the verdict rather than always being ✅, so a "Not confirmed"
// result does not masquerade as a success.
func formatConfirmation(v confirmationView) string {
	// Header reflects the outcome: a non-confirmation never reads "CONFIRMATION".
	lead, status := "❌", "NO CONFIRMATION"
	switch v.verdict {
	case "Reversal confirmed":
		lead, status = "✅", "CONFIRMATION"
	case "Absorbed — weak":
		lead, status = "🟡", "WEAK CONFIRMATION"
	case "Inconclusive — no candle close":
		lead, status = "⚪", "NO CONFIRMATION"
	case "Pending — watching reclaim":
		lead, status = "⏳", "EARLY READ"
	}

	closeStr := "n/a"
	if v.closeOK {
		closeStr = "$" + comma2(v.closePx)
	}

	// When the close could not be fetched, the reclaim was not evaluated — show ⚪
	// (N/A), not ❌, so it does not look like a measured failure.
	reclaimGlyph := metricFail
	switch {
	case !v.closeOK:
		reclaimGlyph = metricNA
	case v.reclaim:
		reclaimGlyph = metricPass
	}

	// Signal agreement: how many of the EVALUABLE signals (N/A excluded) pointed to
	// a reversal. This is a transparent tally, NOT a backtested probability — it
	// surfaces cases like "price didn't reclaim, but both flow signals agreed",
	// which a binary verdict would otherwise bury.
	passed, evaluable := 0, 0
	for _, m := range []metricState{reclaimGlyph, v.cvd, v.spot} {
		if m == metricNA {
			continue
		}
		evaluable++
		if m == metricPass {
			passed++
		}
	}
	tail := ""
	if evaluable > 0 {
		pct := int(math.Round(float64(passed) / float64(evaluable) * 100))
		tail = fmt.Sprintf("\nSignals: %d/%d agree (%d%%)", passed, evaluable, pct)
	}
	if v.watching {
		tail += fmt.Sprintf("\n⏳ Watching up to %d more candles for a reclaim…", v.watchCandles)
	}

	// Candle labelled by its open time (UTC), per the 5-minute candle convention.
	// The matrix rows go in a code fence so the columns align; each row's only
	// emoji is the leading glyph, identical-width across rows.
	row := func(glyph, label, detail string) string {
		return fmt.Sprintf("%s %-14s%s\n", glyph, label, detail)
	}
	return fmt.Sprintf(
		"%s %s · T+%dm · candle %s UTC\n\n"+
			"```\n"+
			"Pair   BTC/USDT\n"+
			"Close  %s\n"+
			"%s%s%s"+
			"──────────────────────\n"+
			"Verdict: %s%s\n"+
			"```",
		lead, status, v.minutesWaited, v.candleStart.UTC().Format("15:04"),
		closeStr,
		row(reclaimGlyph.glyph(), "Price Reclaim", "> $"+comma2(v.flushRangeHigh)),
		row(v.cvd.glyph(), "CVD Inflow", "net positive"),
		row(v.spot.glyph(), "Spot vs Perp", "spot leading"),
		v.verdict, tail,
	)
}

// reclaimResult is the outcome of the stage-2 reclaim watch (notification 2).
type reclaimResult struct {
	reclaimed      bool
	minutesWaited  int
	candleStart    time.Time // the candle that reclaimed (when reclaimed)
	closePx        float64
	closeOK        bool
	flushRangeHigh float64
	candlesWatched int
}

// formatReclaimResult renders the final reclaim-watch message (the second, later
// notification). It is only sent when the early read was still pending.
func formatReclaimResult(r reclaimResult) string {
	if r.reclaimed {
		return fmt.Sprintf(
			"✅ RECLAIM CONFIRMED · T+%dm · candle %s UTC\n\n"+
				"```\n"+
				"Pair   BTC/USDT\n"+
				"Close  $%s\n"+
				"Reclaimed flush high $%s after %d candle(s).\n"+
				"```",
			r.minutesWaited, r.candleStart.UTC().Format("15:04"),
			comma2(r.closePx), comma2(r.flushRangeHigh), r.candlesWatched)
	}
	lastClose := "n/a"
	if r.closeOK {
		lastClose = "$" + comma2(r.closePx)
	}
	return fmt.Sprintf(
		"❌ NOT CONFIRMED · T+%dm · watched %d candles\n\n"+
			"```\n"+
			"Pair   BTC/USDT\n"+
			"Price never closed back above $%s.\n"+
			"Last close %s\n"+
			"```",
		r.minutesWaited, r.candlesWatched, comma2(r.flushRangeHigh), lastClose)
}
