package smaretest

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

	regime         int  // regimeNone until a cross or warm boot sets it
	armed          bool // true == ARMED, false == IDLE
	reArmed        bool // first touch after a cross/re-arm may fire
	havePrev       bool // a previous bar's SMAs are known (for cross detection)
	prevFast       float64
	prevSlow       float64
	barsSinceCross int
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
	flagTight          bool    // the preceding consolidation is a tight, contracting flag
	flagPole           bool    // a real, trend-aligned pole precedes that consolidation
	flagRangePct       float64 // recent-half flag height as % of price (for the message)
	flagPolePct        float64 // signed pole height as % of price (for the message)
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
	// decision can require a tight, contracting flag — the model's entry premise.
	fw := m.ind.flag(m.cfg.FlagLookback, m.cfg.PoleLookback)
	flagRangePct, flagPolePct := 0.0, 0.0
	if fw.ok && fw.refPrice > 0 {
		flagRangePct = fw.recentRange / fw.refPrice * 100
		flagPolePct = fw.poleMove / fw.refPrice * 100
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
		flagPole:     validPole(m.cfg, fw, m.regime),
		flagRangePct: flagRangePct,
		flagPolePct:  flagPolePct,
	})
	// Remember this bar's SMAs so the next bar can detect a sign flip.
	m.prevFast, m.prevSlow, m.havePrev = fast, slow, true
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
	}

	if !m.armed {
		return
	}

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
	// touch only counts as an entry when it is preceded by a tight, contracting flag
	// (the model's premise); a geometric touch that fails the flag gate leaves the
	// setup ARMED so it keeps waiting for the real entry instead of being consumed.
	armedAtStart := m.reArmed
	if armedAtStart {
		longTouch := m.regime == regimeLong && c.bar.Low <= c.fast+c.band && c.bar.Close >= c.fast
		shortTouch := m.regime == regimeShort && c.bar.High >= c.fast-c.band && c.bar.Close <= c.fast
		// The model entry is a pole FOLLOWED BY a tight, contracting flag — both halves
		// are required. A tight consolidation with no pole is just chop reverting to the
		// mean and must not fire (that was the false-trigger bug).
		flagOK := !m.cfg.RequireTightFlag || (c.flagTight && c.flagPole)
		if (longTouch || shortTouch) && flagOK {
			if longTouch && m.cfg.longEnabled() {
				m.send(buildTouch(m.cfg, regimeLong, c, m.barsSinceCross))
			}
			if shortTouch && m.cfg.shortEnabled() {
				m.send(buildTouch(m.cfg, regimeShort, c, m.barsSinceCross))
			}
			m.reArmed = false
			if m.cfg.ReArmMode == ReArmFirstOnly {
				m.disarm()
			}
		}
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
