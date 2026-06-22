package smaretest

import (
	"fmt"
	"log"
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
// decide() so it can be unit-tested without building 200-bar SMA series.
type machine struct {
	cfg  Config
	ind  *indicators
	send func(string)

	regime         int     // regimeNone until a cross or warm boot sets it
	armed          bool    // true == ARMED, false == IDLE
	reArmed        bool    // first touch after a cross/re-arm may fire
	havePrev       bool    // a previous bar's SMAs are known (for cross detection)
	prevFast       float64
	prevSlow       float64
	barsSinceCross int
	maxSep         float64 // max separation of price from the 21 SMA (trend dir, fraction of price) since the cross
}

func newMachine(cfg Config, ind *indicators, send func(string)) *machine {
	return &machine{cfg: cfg, ind: ind, send: send}
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
	flagTight          bool    // the preceding consolidation is a tight, contracting range
	flagRangePct       float64 // recent-half range height as % of price (for the message)
	sepPct             float64 // max separation from the 21 SMA since the cross, as % of price (set in decide, for the message)
}

// processBar is the live entry point: push the bar, recompute indicators, and run
// the decision logic. It returns early (doing nothing) until indicators are ready.
func (m *machine) processBar(b Bar) {
	m.ind.push(b)
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
	// Measure the consolidation that precedes this (touch-candidate) bar so the
	// decision can require a tight, contracting range — the model's entry premise.
	fw := m.ind.flag(m.cfg.FlagLookback)
	flagRangePct := 0.0
	if fw.ok && fw.refPrice > 0 {
		flagRangePct = fw.recentRange / fw.refPrice * 100
	}
	m.decide(barCtx{
		fast:         fast,
		slow:         slow,
		prevFast:     m.prevFast,
		prevSlow:     m.prevSlow,
		havePrev:     m.havePrev,
		band:         band,
		bar:          b,
		flagTight:    tightFlag(m.cfg, fw),
		flagRangePct: flagRangePct,
	})
	// Remember this bar's SMAs so the next bar can detect a sign flip.
	m.prevFast, m.prevSlow, m.havePrev = fast, slow, true
}

// updateMaxSep grows the running peak separation of price from the 21 SMA in the
// trend direction since the cross (long: high above the 21; short: low below it).
func (m *machine) updateMaxSep(fast float64, b Bar) {
	if fast <= 0 || m.regime == regimeNone {
		return
	}
	var sep float64
	if m.regime == regimeLong {
		sep = (b.High - fast) / fast
	} else {
		sep = (fast - b.Low) / fast
	}
	if sep > m.maxSep {
		m.maxSep = sep
	}
}

// decide implements the §4 state machine for a single finalized bar. It mutates
// regime/armed/reArmed and calls send() for touches (and optional invalidations).
func (m *machine) decide(c barCtx) {
	m.barsSinceCross++ // one more bar has elapsed since the last cross

	// (a) Cross detection (bar-close): a sign flip of (SMA21 - SMA200) sets the
	// regime and (re)arms. No separate "cross" alert (L4) — only touches alert.
	if c.havePrev && sign2(c.prevFast-c.prevSlow) != sign2(c.fast-c.slow) {
		if c.fast-c.slow > 0 {
			m.regime = regimeLong
		} else {
			m.regime = regimeShort
		}
		m.armed = true
		m.reArmed = true
		m.barsSinceCross = 0
		m.maxSep = 0 // separation is measured from this new trend, not the last one
	}

	if !m.armed {
		return
	}

	// Separation ("price moves away from these lines"): read the peak excursion from
	// the 21 SMA reached BEFORE this bar, then fold this bar in for future bars. Using
	// the pre-update value means the touch bar's own small excursion past the 21 can
	// never, by itself, satisfy the gate. sepPct is carried on c for the message.
	sepAtStart := m.maxSep
	m.updateMaxSep(c.fast, c.bar)
	separated := sepAtStart*100 >= m.cfg.MinSeparationPct
	c.sepPct = sepAtStart * 100

	// (b) Invalidation: a pullback that reaches the 200 SMA disarms the regime.
	if m.regime == regimeLong && c.bar.Low <= c.slow {
		m.disarm()
		if m.cfg.EmitInvalidation {
			m.send(buildInvalidation(m.cfg, regimeLong, c))
		}
		return
	}
	if m.regime == regimeShort && c.bar.High >= c.slow {
		m.disarm()
		if m.cfg.EmitInvalidation {
			m.send(buildInvalidation(m.cfg, regimeShort, c))
		}
		return
	}

	// (c) Touch detection — evaluated against the arm state AS OF BAR START, so a
	// single bar can never both re-arm and fire. The wick-out filter requires the
	// close back on the correct side of the 21 SMA, not just a wick through it. The
	// touch only counts as an entry when price first moved away from the lines and
	// then tightened into a contracting range (the model's premise); a geometric
	// touch that fails this gate leaves the setup ARMED so it keeps waiting for the
	// real entry instead of being consumed.
	armedAtStart := m.reArmed
	// A "wick touch" is the candle reaching the 21 SMA band; a full touch additionally
	// requires the close back on the correct side (the wick-out filter). Splitting them
	// lets us trace a touch that was seen on the chart but rejected on the close.
	wickLong := m.regime == regimeLong && c.bar.Low <= c.fast+c.band
	wickShort := m.regime == regimeShort && c.bar.High >= c.fast-c.band
	longTouch := wickLong && c.bar.Close >= c.fast
	shortTouch := wickShort && c.bar.Close <= c.fast
	// The model entry is a MOVE AWAY from the lines FOLLOWED BY a tight, contracting
	// range — both halves are required. A tight consolidation that never left the 21
	// is just chop reverting to the mean and must not fire (that was the false trigger).
	flagOK := !m.cfg.RequireTightFlag || (separated && c.flagTight)
	fired := false
	if armedAtStart {
		if (longTouch || shortTouch) && flagOK {
			if longTouch && m.cfg.longEnabled() {
				m.send(buildTouch(m.cfg, regimeLong, c, m.barsSinceCross))
				fired = true
			}
			if shortTouch && m.cfg.shortEnabled() {
				m.send(buildTouch(m.cfg, regimeShort, c, m.barsSinceCross))
				fired = true
			}
			m.reArmed = false
			if m.cfg.ReArmMode == ReArmFirstOnly {
				m.disarm()
			}
		}
	}
	// Near-miss trace: only when the candle actually reached the 21 SMA band but no
	// alert went out, so a "it touched, why no alert?" is answerable from the journal
	// without per-bar spam.
	if (wickLong || wickShort) && !fired {
		m.logSuppressedTouch(c, longTouch || shortTouch, separated, armedAtStart)
	}

	// (d) Re-arm gate for the NEXT bar (debounce): require a close that left the band.
	if !m.reArmed {
		if m.regime == regimeLong && c.bar.Close > c.fast+c.band {
			m.reArmed = true
		}
		if m.regime == regimeShort && c.bar.Close < c.fast-c.band {
			m.reArmed = true
		}
	}
}

// disarm sets state = IDLE; a new cross re-arms.
func (m *machine) disarm() {
	m.armed = false
	m.reArmed = false
}

// logSuppressedTouch explains why a candle that reached the 21 SMA band did NOT
// alert. It names the single dominant cause so the journal answers "it touched the
// 21 — why no alert?" directly. closeOK is true when the close was already on the
// correct side (i.e. the wick-out filter passed and the block was elsewhere).
func (m *machine) logSuppressedTouch(c barCtx, closeOK, separated, armedAtStart bool) {
	var reason string
	switch {
	case !armedAtStart:
		reason = "setup not re-armed yet — price has not closed back outside the 21 SMA band since the last touch"
	case !closeOK:
		reason = "wicked the 21 SMA but CLOSED on the wrong side — support/resistance did not hold (wick-out filter)"
	case !separated:
		reason = fmt.Sprintf("price never moved far enough from the 21 since the cross (peak %.2f%% < %.2f%%) — chop, not a move-away", c.sepPct, m.cfg.MinSeparationPct)
	case !c.flagTight:
		reason = "range not tight/contracting yet"
	default:
		reason = "direction disabled for this regime"
	}
	dist := (c.bar.Close - c.fast) / c.fast * 100
	log.Printf("[SMARETEST] %s TOUCH suppressed: %s | close=%.2f 21SMA=%.2f (%+.2f%%) regime=%s armed=%t reArmed=%t separated=%t sep=%.2f%% flagTight=%t flagRange=%.2f%%",
		c.bar.BucketStart.Format("15:04"), reason, c.bar.Close, c.fast, dist,
		regimeName(m.regime), m.armed, armedAtStart, separated, c.sepPct,
		c.flagTight, c.flagRangePct)
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
