// Package sweep is a self-contained liquidity-sweep alert for BTCUSDT. It watches
// closed 5m candles for a textbook stop run: price trades through a liquidity level
// (a 1h swing, or the previous day's or week's high/low), closes back inside it with a
// large rejection wick on heavy volume, while liquidations on the swept side (Bybit +
// OKX, streamed live) confirm that stops and leveraged positions were actually flushed.
//
// It is deliberately strict and makes no promise of a reversal: backtests on 90 days
// of BTC data found that candle shape, volume, open interest and taker flow alone do
// not predict one, and liquidation history is not available to test. Every alert is
// therefore labelled as a detected sweep and its outcome is logged, so the rules can
// be judged — and tightened or switched off — on real results.
//
// REMOVAL / TWEAKS: the package owns all of its connections, state and messages and
// shares nothing with the other alerts. It is wired in with one call in main.go —
// delete that call (and this folder) to remove it. Every threshold lives in Config;
// LogOnly keeps it running without sending anything.
package sweep

import "time"

// Config is the single source of truth for the module; the comments give the defaults.
type Config struct {
	Symbol       string // "BTCUSDT" (Bybit linear perp)
	Timeframe    string // "5m" — the sweep candle timeframe
	WarmBootBars int    // 2400 (~8 days of 5m) — replayed silently on startup to rebuild the levels

	// Liquidity levels: where stops rest.
	SwingStrength    int     // 2 — a 1h swing needs this many hours on each side that did not go beyond it
	EqualLevelTolPct float64 // 0.05 — levels of the same kind within this % merge into one (equal highs/lows)
	MaxLevelAgeHours int     // 168 — older levels are dropped

	// The sweep candle: through the level, and back.
	MinPenetrationATR float64 // 0.05 — the wick must clear every swept level by at least this much…
	MaxPenetrationATR float64 // 1.5 — …but not by more: that is a breakdown that bounced, not a stop run
	MinWickRatio      float64 // 0.5 — the rejection wick must be at least this share of the candle's range
	MinWickATR        float64 // 1.0 — and at least this many ATRs long
	ATRPeriod         int     // 14
	MinVolumeRatio    float64 // 2.5 — candle volume vs the median of the previous VolumeLookback candles
	VolumeLookback    int     // 48 (4h of 5m candles)

	// Liquidations: proof that positions were flushed on the swept side (longs for a
	// swept low, shorts for a swept high), summed over Bybit and OKX within the candle.
	RequireLiquidations bool    // true — never alert without them
	MinLiqUSD           float64 // 250000 — swept-side liquidations in the candle
	MinLiqSideShare     float64 // 0.7 — the swept side's share of all liquidations in the candle
	LiqGraceSec         int     // 5 — wait after the candle closes for late liquidation messages

	CooldownMin int  // 60 — at most one alert per direction per this many minutes
	LogOnly     bool // false — true logs alerts to the journal instead of sending them

	// Outcome logging: after each alert, whether price hit the invalidation (the wick
	// extreme) and how far it moved in R, at these horizons.
	OutcomeHorizonsMin []int // {15, 30, 60, 240}

	KlineFetchTimeoutSec int // 10
	KlineMaxRetries      int // 3
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Symbol:               "BTCUSDT",
		Timeframe:            "5m",
		WarmBootBars:         2400,
		SwingStrength:        2,
		EqualLevelTolPct:     0.05,
		MaxLevelAgeHours:     168,
		MinPenetrationATR:    0.05,
		MaxPenetrationATR:    1.5,
		MinWickRatio:         0.5,
		MinWickATR:           1.0,
		ATRPeriod:            14,
		MinVolumeRatio:       2.5,
		VolumeLookback:       48,
		RequireLiquidations:  true,
		MinLiqUSD:            250000,
		MinLiqSideShare:      0.7,
		LiqGraceSec:          5,
		CooldownMin:          60,
		LogOnly:              false,
		OutcomeHorizonsMin:   []int{15, 30, 60, 240},
		KlineFetchTimeoutSec: 10,
		KlineMaxRetries:      3,
	}
}

// step is the candle duration, defaulting to 5 minutes if Timeframe does not parse.
func (c Config) step() time.Duration {
	if d, err := time.ParseDuration(c.Timeframe); err == nil && d > 0 {
		return d
	}
	return 5 * time.Minute
}
