package smaretest

import (
	"fmt"
	"log"
	"time"
)

// Regime / state values.
const (
	regimeNone  = 0
	regimeLong  = 1
	regimeShort = -1
)

// machine runs the retest state machine. A single goroutine feeds finalized bars
// to processBar, so state transitions are naturally serialized; the indicator ring
// is independently mutex-guarded (§concurrency). The pure decision logic lives in
// decide() so it can be unit-tested with scripted SMA readings instead of 200-bar
// series.
//
// The model is detected as an ordered sequence on closed bars:
//
//	21/200 cross → flagpole → flag → first touch of the 21 SMA
//
// Only a touch that completes that sequence alerts. A pole is consumed by the first
// touch of the 21 after it (alert or not) and by a failed flag, so every alert needs
// its own pole and flag; price chopping around the 21 never alerts on its own.
type machine struct {
	cfg      Config
	ind      *indicators
	send     func(string)
	outcomes *outcomeTracker // labels fired retests and logs their forward returns
	silent   bool            // warm-boot replay: evolve state from history but send and log nothing

	regime         int  // regimeNone until the first observed cross
	armed          bool // a cross was observed and the setup is not invalidated or used up
	havePrev       bool // a previous bar's SMAs are known (for cross detection)
	prevFast       float64
	prevSlow       float64
	barsSinceCross int
	setupsFired    int // alerts sent since the last cross (MaxSetupsPerCross)

	leg      []Bar             // the last MaxPoleBars bars since the last reset: where a pole's origin is searched
	fastHist []float64         // the last SlopeBars+1 fast SMA readings, for the slope rule
	pole     *pole             // the qualified flagpole whose flag is forming; nil while seeking one
	lastFire map[int]time.Time // regime => bar time of the last alert sent in that direction (cooldown timer)
}

// pole is a qualified flagpole and the flag forming after it. Prices are read in the
// trend direction: for a long the origin is the leg's lowest low, the extreme its
// highest high and the flag depth the lowest low since the extreme; a short mirrors
// all three.
type pole struct {
	origin    float64
	extreme   float64
	bars      int     // bars from the origin to the extreme
	extFast   float64 // the 21 SMA on the extreme's bar
	extPct    float64 // distance of the extreme from the 21 SMA, %
	flagBars  int     // bars since the extreme
	flagDepth float64
}

// setupStats summarises a completed pole → flag → touch for the alert and outcome log.
type setupStats struct {
	poleMovePct float64 // pole size, % of the origin price
	poleBars    int     // bars from the pole's origin to its extreme
	poleExtPct  float64 // distance from the 21 SMA at the pole's extreme, %
	flagBars    int     // bars from the extreme to the touch (inclusive)
	retracePct  float64 // share of the pole the flag gave back, %
	catchUpPct  float64 // share of the extreme's gap to the 21 SMA closed by the 21 moving, %
	slopePct    float64 // 21 SMA move over SlopeBars into the touch, % (positive = trend direction)
}

// catchUp is the share of the gap between the pole's extreme and the 21 SMA (as it
// stood on the extreme's bar) that the 21 itself has closed by now. Near 1 the 21
// came to price (a sideways flag); near 0 price came to the 21 (a V).
func catchUp(p *pole, fast, dir float64) float64 {
	gap := (p.extreme - p.extFast) * dir
	if gap <= 0 {
		return 0
	}
	return (fast - p.extFast) * dir / gap
}

func newMachine(cfg Config, ind *indicators, send func(string)) *machine {
	return &machine{
		cfg:      cfg,
		ind:      ind,
		send:     send,
		outcomes: newOutcomeTracker(cfg.OutcomeHorizonsMin),
		lastFire: make(map[int]time.Time),
	}
}

// barCtx is the per-bar input to decide(): the indicator readings for this bar
// plus the raw OHLC and the touch band. Keeping it separate from indicator
// computation makes the decision logic fully testable.
type barCtx struct {
	fast, slow         float64
	prevFast, prevSlow float64
	havePrev           bool
	band               float64
	bar                Bar
}

// processBar is the live entry point: push the bar, recompute indicators, and run
// the decision logic. It returns early (doing nothing) until indicators are ready.
func (m *machine) processBar(b Bar) {
	m.ind.push(b)
	m.outcomes.onBar(b) // resolve any matured forward-return horizons from the live stream
	if !m.ind.ready(m.cfg.SlowPeriod) {
		return
	}
	fast, okF := m.ind.sma(m.cfg.FastPeriod)
	slow, okS := m.ind.sma(m.cfg.SlowPeriod)
	if !okF || !okS {
		return
	}
	band := m.cfg.TouchTolPct / 100 * fast
	if m.cfg.UseATRTolerance {
		if atr, ok := m.ind.atr(m.cfg.ATRPeriod); ok {
			band = m.cfg.ATRMult * atr
		}
	}
	m.decide(barCtx{
		fast:     fast,
		slow:     slow,
		prevFast: m.prevFast,
		prevSlow: m.prevSlow,
		havePrev: m.havePrev,
		band:     band,
		bar:      b,
	})
	// Remember this bar's SMAs so the next bar can detect a sign flip.
	m.prevFast, m.prevSlow, m.havePrev = fast, slow, true
}

// decide implements the state machine for a single finalized bar. It mutates the
// regime and pole/flag state and calls send() for model entries (and optional
// invalidations).
func (m *machine) decide(c barCtx) {
	m.barsSinceCross++ // one more bar has elapsed since the last cross

	// (a) Cross detection (bar-close): a sign flip of (SMA21 - SMA200) sets the
	// regime and arms a fresh search for the model. There is no alert on the cross.
	if c.havePrev && sign2(c.prevFast-c.prevSlow) != sign2(c.fast-c.slow) {
		if c.fast-c.slow > 0 {
			m.regime = regimeLong
		} else {
			m.regime = regimeShort
		}
		m.armed = true
		m.barsSinceCross = 0
		m.setupsFired = 0
		// A pole belongs to one trend. The leg window is kept: the rally that caused
		// the cross may be where the new pole starts.
		m.pole = nil
	}
	m.pushFast(c.fast)
	defer m.pushLeg(c.bar) // this bar joins the pole-origin window only after it has been evaluated

	if !m.armed || !m.enabled(m.regime) {
		return
	}

	// (b) Invalidation: a pullback that reaches the 200 SMA disarms the regime.
	if (m.regime == regimeLong && c.bar.Low <= c.slow) || (m.regime == regimeShort && c.bar.High >= c.slow) {
		m.logf("[SMARETEST] %s %s invalidated: price reached the 200 SMA (%.2f); waiting for the next cross",
			c.bar.BucketStart.Format("15:04"), regimeName(m.regime), c.slow)
		if m.cfg.EmitInvalidation && !m.silent {
			m.send(buildInvalidation(m.cfg, m.regime, c))
		}
		m.disarm()
		return
	}

	// (c) The model, one phase at a time: find a pole, then follow its flag.
	if m.pole == nil {
		m.seekPole(c)
		return
	}
	m.advanceFlag(c)
}

// seekPole qualifies this bar as a flagpole's extreme when it is the furthest price
// in the trend direction since the leg's origin (the most adverse price in the
// MaxPoleBars window), the leg covers at least MinPoleMovePct, and price stands at
// least MinPoleExtPct away from the 21 SMA. The window is what makes the pole
// impulsive: a slow drift cannot cover the distance before its origin scrolls out.
func (m *machine) seekPole(c barCtx) {
	if len(m.leg) == 0 || c.fast <= 0 {
		return
	}
	dir := float64(m.regime)
	originIdx := 0
	for i, b := range m.leg {
		if (adverse(m.regime, m.leg[originIdx])-adverse(m.regime, b))*dir >= 0 {
			originIdx = i // the latest bar holding the most adverse price
		}
	}
	origin := adverse(m.regime, m.leg[originIdx])
	ext := favorable(m.regime, c.bar)
	for _, b := range m.leg[originIdx:] {
		if (favorable(m.regime, b)-ext)*dir >= 0 {
			return // not a new extreme since the origin — a pullback, not a pole
		}
	}
	movePct := (ext - origin) * dir / origin * 100
	extPct := (ext - c.fast) * dir / c.fast * 100
	if movePct < m.cfg.MinPoleMovePct || extPct < m.cfg.MinPoleExtPct {
		return
	}
	m.pole = &pole{origin: origin, extreme: ext, bars: len(m.leg) - originIdx, extFast: c.fast, extPct: extPct, flagDepth: ext}
	m.logf("[SMARETEST] %s %s flagpole: %.2f%% in %d bars, %.2f%% from the 21 SMA — waiting for a flag into the 21",
		c.bar.BucketStart.Format("15:04"), regimeName(m.regime), movePct, m.pole.bars, extPct)
}

// advanceFlag follows the flag after a qualified pole. A new extreme extends the pole
// and restarts the flag; a pullback that gives back too much of the pole, closes
// through the 21 SMA or drags on past MaxFlagBars breaks it. The first bar that
// touches the 21 and closes back on the trend side completes the flag and decides the
// setup: it alerts only if the flag took long enough, pulled back slower than the
// pole, and the 21 is still curving in the trend direction.
func (m *machine) advanceFlag(c barCtx) {
	p, b, dir := m.pole, c.bar, float64(m.regime)
	if ext := favorable(m.regime, b); (ext-p.extreme)*dir > 0 {
		p.bars += p.flagBars + 1
		p.extreme, p.flagBars, p.flagDepth, p.extFast = ext, 0, ext, c.fast
		p.extPct = (ext - c.fast) * dir / c.fast * 100
		return
	}
	p.flagBars++
	if d := adverse(m.regime, b); (p.flagDepth-d)*dir > 0 {
		p.flagDepth = d
	}

	height := (p.extreme - p.origin) * dir
	retrace := (p.extreme - p.flagDepth) * dir / height
	touched := (adverse(m.regime, b)-c.fast)*dir <= c.band
	held := (b.Close-c.fast)*dir >= 0
	switch {
	case retrace > m.cfg.MaxFlagRetrace:
		m.dropPole(c, fmt.Sprintf("the pullback gave back %.0f%% of the pole (max %.0f%%) — a reversal, not a flag",
			retrace*100, m.cfg.MaxFlagRetrace*100))
		return
	case touched && !held:
		m.dropPole(c, "the flag closed through the 21 SMA — it did not hold")
		return
	case !touched && p.flagBars >= m.cfg.MaxFlagBars:
		m.dropPole(c, fmt.Sprintf("no touch of the 21 SMA within %d bars — the flag went stale", m.cfg.MaxFlagBars))
		return
	case !touched:
		return // the flag is still forming
	}

	slopePct, slopeOK := m.slopePct()
	s := setupStats{
		poleMovePct: height / p.origin * 100,
		poleBars:    p.bars,
		poleExtPct:  p.extPct,
		flagBars:    p.flagBars,
		retracePct:  retrace * 100,
		catchUpPct:  catchUp(p, c.fast, dir) * 100,
		slopePct:    slopePct,
	}
	poleSpeed := height / float64(p.bars)
	flagSpeed := (p.extreme - p.flagDepth) * dir / float64(p.flagBars)
	var reason string
	switch {
	case p.flagBars < m.cfg.MinFlagBars:
		reason = fmt.Sprintf("only %d bar(s) after the pole — a snap back, no flag (min %d)", p.flagBars, m.cfg.MinFlagBars)
	case s.catchUpPct < m.cfg.MinSMACatchUp*100:
		reason = fmt.Sprintf("price fell back onto the 21 — the 21 closed only %.0f%% of the gap (min %.0f%%), a V, not a flag",
			s.catchUpPct, m.cfg.MinSMACatchUp*100)
	case flagSpeed > m.cfg.MaxFlagSpeedRatio*poleSpeed:
		reason = fmt.Sprintf("the pullback ran at %.0f%% of the pole's speed (max %.0f%%) — a dump, not a flag",
			flagSpeed/poleSpeed*100, m.cfg.MaxFlagSpeedRatio*100)
	case !slopeOK || slopePct < m.cfg.MinSlopePct:
		reason = fmt.Sprintf("the 21 SMA is not curving %s (%+.3f%% over %d bars, min %.3f%%)",
			slopeWord(m.regime), slopePct, m.cfg.SlopeBars, m.cfg.MinSlopePct)
	case m.cooling(m.regime, b.BucketStart):
		reason = fmt.Sprintf("%s cooldown — a %s retest already alerted %.0fm ago (limit 1 per %dm per direction)",
			regimeName(m.regime), regimeName(m.regime), b.BucketStart.Sub(m.lastFire[m.regime]).Minutes(), m.cfg.CooldownMin)
	}
	if reason != "" {
		m.dropPole(c, "touched the 21 SMA but "+reason)
		return
	}

	if !m.silent {
		m.send(buildTouch(m.cfg, m.regime, c, s, m.barsSinceCross))
		m.outcomes.record(b.BucketStart, b.Close, m.regime == regimeLong, s, m.barsSinceCross)
	}
	m.logf("[SMARETEST] %s %s model entry alerted: pole %.2f%% in %d bars, flag %d bars (%.0f%% retrace), 21 SMA %+.3f%%",
		b.BucketStart.Format("15:04"), regimeName(m.regime), s.poleMovePct, s.poleBars, s.flagBars, s.retracePct, s.slopePct)
	m.lastFire[m.regime] = b.BucketStart
	m.setupsFired++
	m.pole, m.leg = nil, m.leg[:0]
	if m.cfg.MaxSetupsPerCross > 0 && m.setupsFired >= m.cfg.MaxSetupsPerCross {
		m.disarm()
	}
}

// dropPole abandons the current pole and restarts the pole search from this bar, so a
// new pole cannot reuse an origin from before the failed flag.
func (m *machine) dropPole(c barCtx, reason string) {
	m.logf("[SMARETEST] %s %s setup dropped: %s | close=%.2f 21SMA=%.2f",
		c.bar.BucketStart.Format("15:04"), regimeName(m.regime), reason, c.bar.Close, c.fast)
	m.pole, m.leg = nil, m.leg[:0]
}

// slopePct is how far the 21 SMA moved over the last SlopeBars bars, as a % of its
// current value, signed so positive means in the trend direction. ok is false until
// enough readings exist.
func (m *machine) slopePct() (float64, bool) {
	n, k := len(m.fastHist), m.cfg.SlopeBars
	if k <= 0 || n <= k || m.fastHist[n-1] <= 0 {
		return 0, false
	}
	now, then := m.fastHist[n-1], m.fastHist[n-1-k]
	return (now - then) * float64(m.regime) / now * 100, true
}

// pushLeg appends a bar to the pole-origin window, trimmed to MaxPoleBars.
func (m *machine) pushLeg(b Bar) {
	m.leg = append(m.leg, b)
	if n := m.cfg.MaxPoleBars; n > 0 && len(m.leg) > n {
		m.leg = m.leg[len(m.leg)-n:]
	}
}

// pushFast records this bar's fast SMA, keeping SlopeBars+1 readings.
func (m *machine) pushFast(fast float64) {
	m.fastHist = append(m.fastHist, fast)
	if n := m.cfg.SlopeBars + 1; len(m.fastHist) > n {
		m.fastHist = m.fastHist[len(m.fastHist)-n:]
	}
}

// cooling reports whether an alert in this direction already went out less than
// CooldownMin minutes before this bar. It is measured on BAR time, not the wall
// clock, so the gate is deterministic and behaves identically in replay and tests.
// A regime with no alert yet (zero value absent from the map) is never cooling.
func (m *machine) cooling(regime int, barTime time.Time) bool {
	if m.cfg.CooldownMin <= 0 {
		return false
	}
	last, ok := m.lastFire[regime]
	if !ok {
		return false
	}
	return barTime.Sub(last) < time.Duration(m.cfg.CooldownMin)*time.Minute
}

// enabled reports whether alerts in this regime's direction are configured.
func (m *machine) enabled(regime int) bool {
	return (regime == regimeLong && m.cfg.longEnabled()) || (regime == regimeShort && m.cfg.shortEnabled())
}

// disarm sets state = IDLE; a new cross re-arms.
func (m *machine) disarm() {
	m.armed = false
	m.pole = nil
}

// logf logs a journal line unless the machine is silently replaying history.
func (m *machine) logf(format string, args ...any) {
	if !m.silent {
		log.Printf(format, args...)
	}
}

// favorable returns the bar's extreme in the trend direction (high for a long, low
// for a short); adverse returns the extreme against it.
func favorable(regime int, b Bar) float64 {
	if regime == regimeLong {
		return b.High
	}
	return b.Low
}

func adverse(regime int, b Bar) float64 {
	if regime == regimeLong {
		return b.Low
	}
	return b.High
}

// regimeName renders a regime constant for logs.
func regimeName(r int) string {
	switch r {
	case regimeLong:
		return "LONG"
	case regimeShort:
		return "SHORT"
	default:
		return "none"
	}
}

// slopeWord names the 21 SMA direction a regime needs.
func slopeWord(r int) string {
	if r == regimeShort {
		return "down"
	}
	return "up"
}
