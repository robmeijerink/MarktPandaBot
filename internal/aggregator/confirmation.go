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

// run waits for the target candle to close, then evaluates and sends — unless it
// is cancelled first, in which case it exits silently (§7).
func (c *ConfirmationManager) run(ctx context.Context, snap T0Snapshot, myGen int) {
	target := confirmationTarget(c.now(), c.cfg)
	wait := target.Sub(c.now())
	if wait < 0 {
		wait = 0
	}

	select {
	case <-ctx.Done():
		return // superseded by a newer T0; send nothing
	case <-c.after(wait):
	}

	c.evaluateAndSend(snap, target)

	// Natural completion: clear our cancelFunc so a stale cancel is never called,
	// but only if a newer Trigger has not already replaced it.
	c.mu.Lock()
	if c.gen == myGen {
		c.cancel = nil
	}
	c.mu.Unlock()
}

// evaluateAndSend computes the three confirmation metrics for the just-closed
// target candle and dispatches the message.
func (c *ConfirmationManager) evaluateAndSend(snap T0Snapshot, target time.Time) {
	// The candle that closes at `target` opened one interval earlier; that is the
	// fully-closed candle we evaluate.
	candleStart := target.Add(-time.Duration(c.cfg.CandleIntervalSec) * time.Second)

	// Price Reclaim (required): close on PrimaryExchange > captured flush high.
	closePx, ok := c.fetchClose(candleStart)
	reclaim := ok && closePx > snap.FlushRangeHigh

	// CVD Inflow (optional): net signed perp taker volume > 0.
	cvd := metricNA
	if c.flow.PerpActive() {
		if c.flow.PerpCVD(candleStart) > 0 {
			cvd = metricPass
		} else {
			cvd = metricFail
		}
	}

	// Spot-vs-Perp (optional): net spot taker-buy volume > 0.
	spot := metricNA
	if c.flow.SpotActive() {
		if c.flow.SpotNet(candleStart) > 0 {
			spot = metricPass
		} else {
			spot = metricFail
		}
	}

	verdict := computeVerdict(reclaim, cvd, spot)
	minutesWaited := int(math.Round(target.Sub(snap.T0).Minutes()))

	msg := formatConfirmation(confirmationView{
		minutesWaited:  minutesWaited,
		candleStart:    candleStart,
		closePx:        closePx,
		closeOK:        ok,
		flushRangeHigh: snap.FlushRangeHigh,
		reclaim:        reclaim,
		cvd:            cvd,
		spot:           spot,
		verdict:        verdict,
	})
	c.send(msg)
}

// computeVerdict applies the §6 verdict rule:
//   - Reclaim fails                              => "Not confirmed"
//   - Reclaim passes AND (CVD OR Spot passes)    => "Reversal confirmed"
//   - Reclaim passes, both flow signals fail/NA  => "Absorbed — weak"
func computeVerdict(reclaim bool, cvd, spot metricState) string {
	if !reclaim {
		return "Not confirmed"
	}
	if cvd == metricPass || spot == metricPass {
		return "Reversal confirmed"
	}
	return "Absorbed — weak"
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
}

// formatConfirmation renders the independent confirmation message (§7). The lead
// glyph reflects the verdict rather than always being ✅, so a "Not confirmed"
// result does not masquerade as a success.
func formatConfirmation(v confirmationView) string {
	lead := "❌"
	switch v.verdict {
	case "Reversal confirmed":
		lead = "✅"
	case "Absorbed — weak":
		lead = "🟡"
	}

	closeStr := "n/a"
	if v.closeOK {
		closeStr = fmt.Sprintf("$%.2f", v.closePx)
	}

	reclaimGlyph := metricFail
	if v.reclaim {
		reclaimGlyph = metricPass
	}

	// Candle labelled by its open time (UTC), per the 5-minute candle convention.
	return fmt.Sprintf(
		"%s CONFIRMATION (T+%dm, candle %s UTC)\n"+
			"Pair: BTC/USDT | Close: %s\n"+
			"📊 Confirmation Matrix:\n"+
			"%s Price Reclaim (> $%.2f)\n"+
			"%s CVD Inflow (net positive)\n"+
			"%s Spot vs Perp (spot leading)\n\n"+
			"Verdict: %s",
		lead, v.minutesWaited, v.candleStart.UTC().Format("15:04"),
		closeStr,
		reclaimGlyph.glyph(), v.flushRangeHigh,
		v.cvd.glyph(),
		v.spot.glyph(),
		v.verdict,
	)
}
