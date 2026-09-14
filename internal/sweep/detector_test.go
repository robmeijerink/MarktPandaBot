package sweep

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// detectorConfig pins every rule the scenarios below are built around, so retuning
// DefaultConfig cannot silently change what these tests exercise.
func detectorConfig() Config {
	cfg := DefaultConfig()
	cfg.OutcomeHorizonsMin = nil
	cfg.MinPenetrationATR = 0.05
	cfg.MaxPenetrationATR = 1.5
	cfg.MinWickRatio = 0.5
	cfg.MinWickATR = 1.0
	cfg.ATRPeriod = 14
	cfg.MinVolumeRatio = 2.5
	cfg.VolumeLookback = 48
	cfg.RequireLiquidations = true
	cfg.MinLiqUSD = 250000
	cfg.MinLiqSideShare = 0.7
	cfg.CooldownMin = 60
	cfg.LogOnly = false
	return cfg
}

// sweepStart is when the scenario's sweep candle opens: after 48 baseline candles.
var sweepStart = monday.Add(4 * time.Hour)

// scenario is one sweep test: a detector primed with 48 quiet 5m candles (range 1.0 =>
// ATR 1.0, volume 10), a level, live liquidation feeds, and the sweep candle.
type scenario struct {
	d    *detector
	sent []string
	logs bytes.Buffer
	low  bool
}

// mirrorPx reflects a price around 100 so one scripted long scenario doubles as the
// equivalent short one.
func (s *scenario) px(p float64) float64 {
	if s.low {
		return p
	}
	return 200 - p
}

func newScenario(t *testing.T, cfg Config, low bool) *scenario {
	t.Helper()
	s := &scenario{low: low}
	liq := newLiqStore()
	liq.setUp(venueBybit, true, monday)
	liq.setUp(venueOKX, true, monday)
	s.d = newDetector(cfg, liq, func(msg string) { s.sent = append(s.sent, msg) })
	for i := 0; i < 48; i++ {
		s.d.series.push(Bar{Start: monday.Add(time.Duration(i) * 5 * time.Minute), Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10})
	}
	s.d.book.add(s.px(99.0), monday.Add(-12*time.Hour), kindPrevDay, low)
	prev := log.Writer()
	log.SetOutput(&s.logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	return s
}

// liquidate adds liquidations inside the sweep candle: swept-side USD on Bybit and OKX
// plus opposite-side USD on Bybit.
func (s *scenario) liquidate(bybit, okx, opposite float64) {
	at := sweepStart.Add(time.Minute)
	s.d.liq.add(
		liqEvent{at: at, venue: venueBybit, long: s.low, usd: bybit},
		liqEvent{at: at, venue: venueOKX, long: s.low, usd: okx},
		liqEvent{at: at, venue: venueBybit, long: !s.low, usd: opposite},
	)
}

// candle builds the sweep candle from long-side prices (mirrored for shorts).
func (s *scenario) candle(open, extreme, close, volume float64) Bar {
	o, x, c := s.px(open), s.px(extreme), s.px(close)
	b := Bar{Start: sweepStart, Open: o, Close: c, Volume: volume}
	if s.low {
		b.Low, b.High = x, 99.8
	} else {
		b.High, b.Low = x, 200-99.8
	}
	return b
}

// textbook: the wick runs 0.5 ATR through the 99.0 level, is 1.1 ATR and 85% of the
// candle, closes back at 99.7 on 3x volume, with $400k swept-side liquidations (95%).
func (s *scenario) textbook() Bar {
	s.liquidate(300000, 100000, 20000)
	return s.candle(99.6, 98.5, 99.7, 30)
}

func TestTextbookSweepAlerts(t *testing.T) {
	for _, tc := range []struct {
		low  bool
		want []string
	}{
		{true, []string{"LIQUIDITY SWEEP", "BULLISH", "previous day low", "$99", "$400k longs liquidated", "Bybit $300k · OKX $100k", "closed back above", "Invalidated below"}},
		{false, []string{"LIQUIDITY SWEEP", "BEARISH", "previous day high", "$101", "$400k shorts liquidated", "closed back below", "Invalidated above"}},
	} {
		s := newScenario(t, detectorConfig(), tc.low)
		s.d.processBar(s.textbook())
		if len(s.sent) != 1 {
			t.Fatalf("low=%t: a textbook sweep must send exactly one alert, got %d; log:\n%s", tc.low, len(s.sent), s.logs.String())
		}
		for _, w := range tc.want {
			if !strings.Contains(s.sent[0], w) {
				t.Errorf("low=%t: alert is missing %q:\n%s", tc.low, w, s.sent[0])
			}
		}
		if lvls := s.d.book.lows; tc.low && len(lvls) != 0 {
			t.Errorf("the swept level must be spent, still have %v", prices(lvls))
		}
	}
}

// Every rule on its own blocks the alert, for the reason it is named after, and the
// pierced level is spent either way.
func TestEachRuleRejects(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		setup  func(s *scenario) Bar
	}{
		{"closes through the level", "a break, not a sweep", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 20000)
			return s.candle(99.6, 98.5, 98.9, 30)
		}},
		{"barely pokes through", "barely poked through", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 20000)
			return s.candle(99.6, 98.98, 99.7, 30)
		}},
		{"runs far beyond the level", "a breakdown that bounced", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 20000)
			return s.candle(99.6, 97.3, 99.7, 30)
		}},
		{"body-heavy candle", "of the candle (min 50%)", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 20000)
			return s.candle(98.6, 98.5, 99.7, 30)
		}},
		{"short wick", "wick is 0.90 ATR", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 20000)
			return s.candle(99.6, 98.7, 99.7, 30)
		}},
		{"normal volume", "volume 2.0x normal", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 20000)
			return s.candle(99.6, 98.5, 99.7, 20)
		}},
		{"liquidation feed down", "Bybit liquidation feed was not connected", func(s *scenario) Bar {
			s.d.liq.setUp(venueBybit, false, sweepStart)
			s.d.liq.setUp(venueBybit, true, sweepStart.Add(time.Minute)) // reconnected mid-candle
			s.liquidate(300000, 100000, 20000)
			return s.candle(99.6, 98.5, 99.7, 30)
		}},
		{"too few liquidations", "liquidated $200k (min $250k)", func(s *scenario) Bar {
			s.liquidate(150000, 50000, 0)
			return s.candle(99.6, 98.5, 99.7, 30)
		}},
		{"two-sided liquidations", "two-sided flush", func(s *scenario) Bar {
			s.liquidate(300000, 100000, 300000)
			return s.candle(99.6, 98.5, 99.7, 30)
		}},
		{"cooldown", "cooldown", func(s *scenario) Bar {
			s.d.lastFire[s.low] = sweepStart.Add(-30 * time.Minute)
			return s.textbook()
		}},
	}
	for _, tc := range cases {
		for _, low := range []bool{true, false} {
			s := newScenario(t, detectorConfig(), low)
			s.d.processBar(tc.setup(s))
			if len(s.sent) != 0 {
				t.Errorf("%s (low=%t): must not alert, got %v", tc.name, low, s.sent)
			}
			if !strings.Contains(s.logs.String(), tc.reason) {
				t.Errorf("%s (low=%t): want rejection reason %q, log:\n%s", tc.name, low, tc.reason, s.logs.String())
			}
			if len(s.d.book.lows)+len(s.d.book.highs) != 0 {
				t.Errorf("%s (low=%t): the pierced level must be spent", tc.name, low)
			}
		}
	}
}

// Liquidations outside the candle's own window do not count.
func TestLiquidationsOutsideTheCandleDoNotCount(t *testing.T) {
	s := newScenario(t, detectorConfig(), true)
	s.d.liq.add(
		liqEvent{at: sweepStart.Add(-time.Second), venue: venueBybit, long: true, usd: 900000},
		liqEvent{at: sweepStart.Add(5 * time.Minute), venue: venueBybit, long: true, usd: 900000},
	)
	s.d.processBar(s.candle(99.6, 98.5, 99.7, 30))
	if len(s.sent) != 0 {
		t.Fatalf("liquidations before or after the candle must not confirm it, got %v", s.sent)
	}
}

// The tweak switches: RequireLiquidations=false alerts on the candle alone; LogOnly
// logs the alert without sending it.
func TestSwitches(t *testing.T) {
	cfg := detectorConfig()
	cfg.RequireLiquidations = false
	s := newScenario(t, cfg, true)
	s.d.processBar(s.candle(99.6, 98.5, 99.7, 30))
	if len(s.sent) != 1 {
		t.Fatalf("RequireLiquidations=false should alert without liquidations, got %d", len(s.sent))
	}

	cfg = detectorConfig()
	cfg.LogOnly = true
	s2 := newScenario(t, cfg, true)
	s2.d.processBar(s2.textbook())
	if len(s2.sent) != 0 || !strings.Contains(s2.logs.String(), "LogOnly, not sent") {
		t.Fatalf("LogOnly must log instead of sending: sent=%d log:\n%s", len(s2.sent), s2.logs.String())
	}
}

// A level alerts at most once: the same sweep candle again finds nothing to sweep.
func TestLevelAlertsOnce(t *testing.T) {
	cfg := detectorConfig()
	cfg.CooldownMin = 0
	s := newScenario(t, cfg, true)
	b := s.textbook()
	s.d.processBar(b)
	b.Start = b.Start.Add(5 * time.Minute)
	s.d.processBar(b)
	if len(s.sent) != 1 {
		t.Fatalf("a spent level must not alert twice, got %d", len(s.sent))
	}
}

// Silent replay never sends or logs, but still spends levels and starts cooldowns so a
// restart cannot re-alert a sweep that already happened.
func TestSilentReplay(t *testing.T) {
	s := newScenario(t, detectorConfig(), true)
	replay(s.d, []Bar{s.textbook()})
	if len(s.sent) != 0 || s.logs.Len() != 0 {
		t.Fatalf("replay must be silent: sent=%d log=%q", len(s.sent), s.logs.String())
	}
	if s.d.silent || len(s.d.book.lows) != 0 || !s.d.lastFire[true].Equal(sweepStart) {
		t.Fatalf("replay must spend the level and start the cooldown: silent=%t lows=%v lastFire=%v", s.d.silent, prices(s.d.book.lows), s.d.lastFire)
	}
}

// Without enough history for the ATR and volume baselines nothing can alert, but the
// level is still spent.
func TestNoAlertBeforeBaselinesAreReady(t *testing.T) {
	cfg := detectorConfig()
	liq := newLiqStore()
	liq.setUp(venueBybit, true, monday)
	var sent []string
	d := newDetector(cfg, liq, func(msg string) { sent = append(sent, msg) })
	d.book.add(99.0, monday, kindPrevDay, true)
	d.processBar(Bar{Start: sweepStart, Open: 99.6, High: 99.8, Low: 98.5, Close: 99.7, Volume: 30})
	if len(sent) != 0 || len(d.book.lows) != 0 {
		t.Fatalf("no baselines => no alert, level spent: sent=%d lows=%v", len(sent), prices(d.book.lows))
	}
}
