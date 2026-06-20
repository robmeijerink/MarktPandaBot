package aggregator

import (
	"fmt"
	"log"
	"time"
)

// OutcomeLogger records, for every dispatched alert, the T0 feature vector and the
// forward return at several horizons. None of the T0 signals are validated edges
// (see config.go), so the only honest way to learn which ones actually predict a
// reversal is to label real alerts and measure. It emits two kinds of structured,
// grep-friendly stdout lines:
//
//   - [OUTCOME-T0]  one per alert, with every signal's pass/fail and raw value.
//   - [OUTCOME-FWD] one per (alert, horizon), with the realised forward return and
//     whether it moved in the predicted reversal direction.
//
// Join the two on id=<ts> to build a hit-rate table per signal and per combination.
type OutcomeLogger struct {
	fetchClose CloseFunc
	horizons   []time.Duration

	// Injectable seams for tests.
	now   func() time.Time
	after func(d time.Duration) <-chan time.Time
	emit  func(string) // defaults to log.Printf("%s", line)
}

// NewOutcomeLogger builds a logger for the given forward horizons (minutes). It
// reuses the same CloseFunc the confirmation manager uses (Bybit close with OKX
// fallback), so it needs no exchange dependency.
func NewOutcomeLogger(fetchClose CloseFunc, horizonsMin []int) *OutcomeLogger {
	hs := make([]time.Duration, 0, len(horizonsMin))
	for _, m := range horizonsMin {
		if m > 0 {
			hs = append(hs, time.Duration(m)*time.Minute)
		}
	}
	return &OutcomeLogger{
		fetchClose: fetchClose,
		horizons:   hs,
		now:        func() time.Time { return time.Now().UTC() },
		after:      time.After,
		emit:       func(s string) { log.Printf("%s", s) },
	}
}

// OutcomeSnapshot is the labelled feature vector for one alert.
type OutcomeSnapshot struct {
	T0            time.Time
	BaselinePrice float64
	ReversalUp    bool // true => a reversal UP is expected (long flush); false => DOWN
	Score         SetupScore
	OIChangePct   float64
	LongSharePct  float64
	VolRatio      float64 // bucketVol / median (0 when warming)
	FundingPct    float64
	PerpCVD       float64
}

// id is the stable join key for a snapshot's T0 and forward lines.
func (s OutcomeSnapshot) id() string { return s.T0.Format("20060102T150405Z") }

// Record emits the T0 feature line immediately and, in the background, the forward
// return at each configured horizon. It returns at once; the forward reads run in a
// goroutine. Safe to call with a nil/zero baseline — forward returns are then logged
// as n/a rather than dividing by zero.
func (o *OutcomeLogger) Record(snap OutcomeSnapshot) {
	dir := "down"
	if snap.ReversalUp {
		dir = "up"
	}
	o.emit(fmt.Sprintf(
		"[OUTCOME-T0] id=%s dir=%s score=%d/%d oi=%s skew=%s vol=%s fund=%s cvd=%s "+
			"oiPct=%.2f longShare=%.1f volRatio=%.2f fundingPct=%.4f perpCVD=%.0f price=%.2f",
		snap.id(), dir, snap.Score.Total, snap.Score.Max,
		passMark(snap.Score.OIDrop), passMark(snap.Score.Skew), passMark(snap.Score.VolSpike),
		passMark(snap.Score.Funding), cvdMark(snap.Score),
		snap.OIChangePct, snap.LongSharePct, snap.VolRatio, snap.FundingPct*100,
		snap.PerpCVD, snap.BaselinePrice))

	if len(o.horizons) == 0 {
		return
	}
	go o.trackForward(snap)
}

// trackForward waits out each horizon and logs the realised forward return. It
// reads the close of the 5-minute candle containing T0+horizon on the primary
// exchange, the same reference the confirmation stage uses.
func (o *OutcomeLogger) trackForward(snap OutcomeSnapshot) {
	for _, h := range o.horizons {
		target := snap.T0.Add(h).Add(confirmSettleDelay)
		wait := target.Sub(o.now())
		if wait < 0 {
			wait = 0
		}
		<-o.after(wait)

		bucket := floorTo5Min(snap.T0.Add(h))
		px, ok := o.fetchClose(bucket)
		if !ok || snap.BaselinePrice <= 0 {
			o.emit(fmt.Sprintf("[OUTCOME-FWD] id=%s h=%dm ret=n/a favorable=n/a",
				snap.id(), int(h.Minutes())))
			continue
		}
		ret := (px - snap.BaselinePrice) / snap.BaselinePrice * 100
		favorable := (snap.ReversalUp && ret > 0) || (!snap.ReversalUp && ret < 0)
		o.emit(fmt.Sprintf("[OUTCOME-FWD] id=%s h=%dm price=%.2f ret=%+.2f%% favorable=%t",
			snap.id(), int(h.Minutes()), px, ret, favorable))
	}
}

// cvdMark renders the CVD item for the log line, distinguishing N/A from a fail.
func cvdMark(s SetupScore) string {
	if s.CVDNA {
		return "na"
	}
	return passMark(s.CVD)
}
