package smaretest

import (
	"strings"
	"testing"
	"time"
)

var testT0 = time.UnixMilli(1_700_000_000_000).UTC()

func bar(o, h, l, c float64) Bar { return Bar{Open: o, High: h, Low: l, Close: c} }

// testConfig pins every model threshold the scripted scenarios are built around, so
// retuning DefaultConfig does not silently change what these tests exercise.
func testConfig() Config {
	cfg := DefaultConfig()
	cfg.OutcomeHorizonsMin = nil
	cfg.TouchTolPct = 0.04
	cfg.UseATRTolerance = false
	cfg.MinPoleMovePct = 0.5
	cfg.MaxPoleBars = 10
	cfg.MinPoleExtPct = 0.2
	cfg.MinFlagBars = 3
	cfg.MaxFlagBars = 15
	cfg.MaxFlagRetrace = 0.7
	cfg.MaxFlagSpeedRatio = 1.0
	cfg.MinSMACatchUp = 0.33
	cfg.SlopeBars = 3
	cfg.MinSlopePct = 0.01
	cfg.MaxSetupsPerCross = 1
	cfg.CooldownMin = 15
	return cfg
}

// step is one scripted bar: its OHLC plus the 21/200 SMA readings on that bar.
type step struct {
	fast, slow float64
	b          Bar
}

// script drives decide() with explicit SMA readings, one minute per bar, carrying
// each bar's SMAs into the next as prevFast/prevSlow exactly like processBar does.
type script struct {
	m    *machine
	sent []string
	n    int
}

func newScript(cfg Config) *script {
	s := &script{}
	s.m = newMachine(cfg, newIndicators(cfg), func(msg string) { s.sent = append(s.sent, msg) })
	return s
}

func (s *script) feed(steps ...step) {
	for _, st := range steps {
		st.b.BucketStart = testT0.Add(time.Duration(s.n) * time.Minute)
		s.n++
		s.m.decide(barCtx{
			fast: st.fast, slow: st.slow,
			prevFast: s.m.prevFast, prevSlow: s.m.prevSlow, havePrev: s.m.havePrev,
			band: s.m.cfg.TouchTolPct / 100 * st.fast,
			bar:  st.b,
		})
		s.m.prevFast, s.m.prevSlow, s.m.havePrev = st.fast, st.slow, true
	}
}

// crossLong is a golden cross with price sitting just above both SMAs.
func crossLong() []step {
	return []step{
		{98.9, 99, bar(99.5, 99.6, 99.4, 99.5)},  // 21 below 200
		{99.1, 99, bar(99.5, 99.6, 99.4, 99.55)}, // golden cross
	}
}

// poleLong launches a 0.9% leg from the 99.4 origin to a 100.3 extreme, 0.9% above the 21.
func poleLong() []step {
	return []step{
		{99.2, 99, bar(99.55, 99.8, 99.5, 99.75)},   // leg starts (0.40% — not a pole yet)
		{99.3, 99, bar(99.75, 100.0, 99.7, 99.95)},  // 0.60% leg, 0.70% above the 21: pole
		{99.4, 99, bar(99.95, 100.3, 99.9, 100.25)}, // new extreme: pole extends
	}
}

// flagLong drifts sideways-down while the rising 21 catches up, touching it on the
// fourth bar with a close back above: the model entry.
func flagLong() []step {
	return []step{
		{99.55, 99, bar(100.25, 100.28, 100.15, 100.2)},
		{99.7, 99, bar(100.2, 100.25, 100.05, 100.1)},
		{99.85, 99, bar(100.1, 100.15, 99.98, 100.05)},
		{100.0, 99, bar(100.05, 100.1, 100.02, 100.06)}, // kisses the 21, support holds
	}
}

func modelLong() []step {
	return concat(crossLong(), poleLong(), flagLong())
}

func concat(parts ...[]step) []step {
	var out []step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// mirror reflects a scenario around price 100, turning a long setup into the
// equivalent short one (a death cross, a pole down, a flag up into a falling 21).
func mirror(steps []step) []step {
	out := make([]step, len(steps))
	for i, s := range steps {
		out[i] = step{200 - s.fast, 200 - s.slow, bar(200-s.b.Open, 200-s.b.Low, 200-s.b.High, 200-s.b.Close)}
	}
	return out
}

// The full cross → pole → flag → touch sequence fires exactly once, on the touch bar,
// for longs and (mirrored) shorts.
func TestModelEntryFires(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"LONG", modelLong()},
		{"SHORT", mirror(modelLong())},
	} {
		s := newScript(testConfig())
		s.feed(tc.steps[:len(tc.steps)-1]...)
		if len(s.sent) != 0 {
			t.Fatalf("%s: nothing may fire before the touch, got %v", tc.name, s.sent)
		}
		if s.m.pole == nil {
			t.Fatalf("%s: a flagpole should be tracked while the flag forms", tc.name)
		}
		s.feed(tc.steps[len(tc.steps)-1])
		if len(s.sent) != 1 || !strings.Contains(s.sent[0], "SMA RETEST — "+tc.name) {
			t.Fatalf("%s: the touch should fire one %s alert, got %v", tc.name, tc.name, s.sent)
		}
	}
}

// Price hugging a rising 21 after the cross never stretches away from it, so there is
// no pole and no touch alerts — however often it kisses the line.
func TestNoPoleNoAlert(t *testing.T) {
	s := newScript(testConfig())
	s.feed(crossLong()...)
	for i := 0; i < 20; i++ {
		f := 99.2 + 0.05*float64(i)
		s.feed(step{f, 99, bar(f+0.05, f+0.1, f+0.01, f+0.05)})
	}
	if len(s.sent) != 0 || s.m.pole != nil {
		t.Fatalf("price riding the 21 is not a pole: alerts=%d pole=%v", len(s.sent), s.m.pole)
	}
}

// A leg that covers the distance too slowly (outside MaxPoleBars) is a drift, not a
// flagpole, even when price is well away from the 21.
func TestSlowDriftIsNoPole(t *testing.T) {
	s := newScript(testConfig())
	s.feed(crossLong()...)
	for i := 0; i < 30; i++ {
		p := 99.6 + 0.025*float64(i)
		s.feed(step{p - 0.35, 99, bar(p, p+0.02, p-0.01, p+0.02)})
		if s.m.pole != nil {
			t.Fatalf("bar %d: a 0.025%%/bar drift must not qualify as a pole", i)
		}
	}
}

// Touching the 21 straight after the pole is a snap back with no flag: no alert, and
// the pole is consumed so a later touch in the same drift cannot use it.
func TestSnapBackIsNoFlag(t *testing.T) {
	s := newScript(testConfig())
	s.feed(concat(crossLong(), poleLong())...)
	s.feed(step{99.9, 99, bar(100.25, 100.28, 99.92, 99.95)}) // touches the 21 one bar after the extreme
	if s.m.pole != nil {
		t.Fatalf("a snap back to the 21 must consume the pole")
	}
	s.feed(
		step{99.95, 99, bar(99.95, 100.1, 99.98, 100.05)},
		step{100.0, 99, bar(100.05, 100.25, 100.1, 100.2)},
		step{100.1, 99, bar(100.2, 100.22, 100.12, 100.15)}, // kisses the 21 again
	)
	if len(s.sent) != 0 {
		t.Fatalf("no flag formed, so no alert may fire, got %v", s.sent)
	}
}

// When the gap closes because price falls onto a sluggish 21 (a V) rather than the 21
// catching up to a sideways flag, the touch does not alert.
func TestPriceFallingOntoThe21IsNoFlag(t *testing.T) {
	s := newScript(testConfig())
	s.feed(concat(crossLong(), poleLong()[:2])...)
	s.feed(
		step{99.8, 99, bar(99.95, 100.3, 99.9, 100.25)}, // extreme, 0.5 above the 21
		step{99.82, 99, bar(100.25, 100.28, 100.1, 100.15)},
		step{99.84, 99, bar(100.15, 100.2, 100.0, 100.05)},
		step{99.86, 99, bar(100.05, 100.1, 99.96, 100.0)},
		step{99.9, 99, bar(100.0, 100.05, 99.92, 99.95)}, // touch: the 21 closed only 20% of the gap
	)
	if len(s.sent) != 0 || s.m.pole != nil {
		t.Fatalf("a V onto the 21 must not alert and must consume the pole: alerts=%d", len(s.sent))
	}
}

// The 21 SMA must still be curving in the trend direction at the touch: a 21 that has
// gone flat rejects an otherwise textbook flag.
func TestFlatSMARejected(t *testing.T) {
	s := newScript(testConfig())
	s.feed(concat(crossLong(), poleLong())...)
	s.feed(
		step{99.6, 99, bar(100.25, 100.28, 100.2, 100.22)},
		step{99.8, 99, bar(100.22, 100.25, 100.15, 100.18)},
		step{100.0, 99, bar(100.18, 100.2, 100.1, 100.12)},
		step{100.0, 99, bar(100.12, 100.15, 100.08, 100.1)},
		step{100.0, 99, bar(100.1, 100.12, 100.06, 100.08)},
		step{100.0, 99, bar(100.08, 100.1, 100.02, 100.05)}, // touch with a flat 21
	)
	if len(s.sent) != 0 {
		t.Fatalf("a flat 21 SMA must not alert, got %v", s.sent)
	}

	// The same flag into a 21 that is still rising fires.
	s2 := newScript(testConfig())
	s2.feed(modelLong()...)
	if len(s2.sent) != 1 {
		t.Fatalf("a rising 21 should fire, got %d", len(s2.sent))
	}
}

// A flag bar that touches the 21 but closes through it breaks the flag; a later clean
// touch has no pole left to complete.
func TestFlagClosingThroughThe21Breaks(t *testing.T) {
	s := newScript(testConfig())
	steps := modelLong()
	s.feed(steps[:len(steps)-1]...)
	s.feed(step{100.0, 99, bar(100.05, 100.1, 99.9, 99.95)})    // closes below the 21
	s.feed(step{100.05, 99, bar(99.95, 100.1, 100.04, 100.08)}) // clean kiss afterwards
	if len(s.sent) != 0 {
		t.Fatalf("a flag that closed through the 21 must not alert, got %v", s.sent)
	}
}

// A flag that never reaches the 21 within MaxFlagBars goes stale and is dropped.
func TestStaleFlagDropped(t *testing.T) {
	cfg := testConfig()
	s := newScript(cfg)
	s.feed(concat(crossLong(), poleLong())...)
	for i := 0; i < cfg.MaxFlagBars; i++ {
		s.feed(step{99.5, 99, bar(100.2, 100.25, 100.15, 100.2)})
	}
	if s.m.pole != nil {
		t.Fatalf("a flag with no touch after %d bars must be dropped", cfg.MaxFlagBars)
	}
	s.feed(step{100.1, 99, bar(100.2, 100.22, 100.12, 100.15)})
	if len(s.sent) != 0 {
		t.Fatalf("a stale flag must not alert, got %v", s.sent)
	}
}

// A pullback that reaches the 200 SMA disarms the regime (silently unless
// EmitInvalidation is on).
func TestInvalidation(t *testing.T) {
	for _, emit := range []bool{false, true} {
		cfg := testConfig()
		cfg.EmitInvalidation = emit
		s := newScript(cfg)
		s.feed(concat(crossLong(), poleLong()[:2])...)
		s.feed(step{99.4, 99, bar(99.5, 99.6, 98.95, 99.1)}) // low reaches the 200 SMA
		if s.m.armed || s.m.pole != nil {
			t.Fatalf("emit=%t: reaching the 200 SMA must disarm", emit)
		}
		want := 0
		if emit {
			want = 1
		}
		if len(s.sent) != want {
			t.Fatalf("emit=%t: want %d invalidation notes, got %v", emit, want, s.sent)
		}
	}
}

// secondSetupLong is a fresh pole and flag right after modelLong's entry, five
// minutes after it.
func secondSetupLong() []step {
	return []step{
		{100.1, 99, bar(100.06, 100.3, 100.05, 100.25)},
		{100.2, 99, bar(100.25, 100.6, 100.2, 100.55)}, // new pole
		{100.3, 99, bar(100.55, 100.58, 100.45, 100.5)},
		{100.4, 99, bar(100.5, 100.52, 100.48, 100.5)},
		{100.45, 99, bar(100.47, 100.5, 100.46, 100.48)}, // kisses the 21
	}
}

// MaxSetupsPerCross keeps it to the first setup after a cross; the cooldown silences a
// second setup in the same direction inside CooldownMin, and neither gate touches the
// other direction.
func TestSetupLimits(t *testing.T) {
	// Default: one setup per cross.
	s := newScript(testConfig())
	s.feed(concat(modelLong(), secondSetupLong())...)
	if len(s.sent) != 1 {
		t.Fatalf("MaxSetupsPerCross=1 should allow exactly one alert, got %d", len(s.sent))
	}

	// Unlimited setups, no cooldown: the second pole + flag fires too.
	cfg := testConfig()
	cfg.MaxSetupsPerCross, cfg.CooldownMin = 0, 0
	s2 := newScript(cfg)
	s2.feed(concat(modelLong(), secondSetupLong())...)
	if len(s2.sent) != 2 {
		t.Fatalf("with no limits the second setup should fire, got %d", len(s2.sent))
	}

	// Unlimited setups, 15m cooldown: the second setup five minutes later is silent.
	cfg.CooldownMin = 15
	s3 := newScript(cfg)
	s3.feed(concat(modelLong(), secondSetupLong())...)
	if len(s3.sent) != 1 {
		t.Fatalf("a second LONG setup inside the cooldown must be silent, got %d", len(s3.sent))
	}

	// A LONG alert a minute ago does not cool down a SHORT setup.
	s4 := newScript(testConfig())
	s4.m.lastFire[regimeLong] = testT0.Add(-time.Minute)
	s4.feed(mirror(modelLong())...)
	if len(s4.sent) != 1 {
		t.Fatalf("the SHORT setup must not be blocked by the LONG cooldown, got %d", len(s4.sent))
	}
}

// Silent replay rebuilds the state without sending: a flag still forming at the end
// of history alerts on the first live touch, and a setup that completed during the
// replay is used up rather than re-alerted.
func TestSilentReplay(t *testing.T) {
	steps := modelLong()

	s := newScript(testConfig())
	s.m.silent = true
	s.feed(steps[:len(steps)-1]...)
	s.m.silent = false
	s.feed(steps[len(steps)-1])
	if len(s.sent) != 1 {
		t.Fatalf("a flag replayed from history should alert on the live touch, got %d", len(s.sent))
	}

	s2 := newScript(testConfig())
	s2.m.silent = true
	s2.feed(steps...)
	s2.m.silent = false
	if len(s2.sent) != 0 {
		t.Fatalf("replay must be silent, got %v", s2.sent)
	}
	if s2.m.armed || s2.m.lastFire[regimeLong].IsZero() {
		t.Fatalf("a setup completed in history must be used up: armed=%t lastFire=%v", s2.m.armed, s2.m.lastFire)
	}
}

// replayHistory runs real bars through processBar: it arms only when it observes a
// cross, and never sends.
func TestReplayHistoryNeedsObservedCross(t *testing.T) {
	cfg := testConfig()
	cfg.FastPeriod, cfg.SlowPeriod, cfg.WarmBootBars = 2, 4, 20
	history := func(closes ...float64) []Bar {
		out := make([]Bar, len(closes))
		for i, c := range closes {
			out[i] = Bar{BucketStart: testT0.Add(time.Duration(i) * time.Minute), Open: c, High: c, Low: c, Close: c}
		}
		return out
	}

	var sent []string
	m := newMachine(cfg, newIndicators(cfg), func(s string) { sent = append(sent, s) })
	replayHistory(m, history(10, 9, 8, 7, 6, 7, 9, 11)) // falls, then crosses up
	if m.regime != regimeLong || !m.armed || m.silent || len(sent) != 0 {
		t.Fatalf("observed golden cross: regime=%d armed=%t silent=%t sent=%d", m.regime, m.armed, m.silent, len(sent))
	}

	m2 := newMachine(cfg, newIndicators(cfg), func(s string) { sent = append(sent, s) })
	replayHistory(m2, history(1, 2, 3, 4, 5, 6, 7, 8)) // uptrend throughout, no cross
	if m2.regime != regimeNone || m2.armed {
		t.Fatalf("no observed cross must stay unarmed: regime=%d armed=%t", m2.regime, m2.armed)
	}
}
