// Package smaretest is a fully self-contained 21/200 SMA pullback/retest alert
// module (see upgrade.md). After a 21/200 SMA cross sets the trend on closed 3m
// candles, it waits for a bar-close touch of the 21 SMA (dynamic support for
// longs / resistance for shorts) and fires a Telegram alert; a pullback all the
// way to the 200 SMA invalidates the setup.
//
// ISOLATION: this package owns all of its state, files and messages. It does not
// read or depend on the liquidation/reversal feature or any existing alert logic.
// It reuses only the standard library and the project's WebSocket dependency, and
// is wired in with a single commented startup call in main.go.
//
// All time math is UTC; the host clock is assumed NTP-synced.
package smaretest

// Direction values for Config.Directions.
const (
	DirBoth  = "both"
	DirLong  = "long"
	DirShort = "short"
)

// Re-arm modes for Config.ReArmMode.
const (
	ReArmDebounce  = "debounce"
	ReArmFirstOnly = "firstOnly"
)

// Config is the single source of truth for the module. The comments give the
// locked/default values from upgrade.md; tuning needs no code change.
type Config struct {
	PrimaryExchange string // "bybit"
	Symbol          string // "BTCUSDT" (perp)
	Timeframe       string // "3m"
	FastPeriod      int    // 21
	SlowPeriod      int    // 200
	WarmBootBars    int    // 300 — must be >= SlowPeriod plus headroom

	Directions string // "both" (locked) | "long" | "short"

	// Touch band around the 21 SMA (TUNABLE).
	UseATRTolerance bool    // false — if true use ATRMult*ATR, else pct band
	TouchTolPct     float64 // 0.05 — percent band (0.05 = 0.05%)
	ATRPeriod       int     // 14
	ATRMult         float64 // 0.25

	// Re-arm / anti-spam (TUNABLE).
	ReArmMode        string // "debounce" (default) | "firstOnly"
	EmitInvalidation bool   // false — send a note when price reaches the 200 SMA

	BarCloseGraceSec int // 5 — wait after candle close before reading the finalized kline

	// Operational knobs for the 3m source (not part of the signal).
	KlineFetchTimeoutSec int // 10 — REST HTTP client timeout
	KlineMaxRetries      int // 3  — warm-boot fetch retries before giving up
}

// DefaultConfig returns the locked/default configuration from upgrade.md.
func DefaultConfig() Config {
	return Config{
		PrimaryExchange:      "bybit",
		Symbol:               "BTCUSDT",
		Timeframe:            "3m",
		FastPeriod:           21,
		SlowPeriod:           200,
		WarmBootBars:         300,
		Directions:           DirBoth,
		UseATRTolerance:      false,
		TouchTolPct:          0.05,
		ATRPeriod:            14,
		ATRMult:              0.25,
		ReArmMode:            ReArmFirstOnly,
		EmitInvalidation:     false,
		BarCloseGraceSec:     5,
		KlineFetchTimeoutSec: 10,
		KlineMaxRetries:      3,
	}
}

// longEnabled reports whether LONG (golden-cross) touches may be emitted.
func (c Config) longEnabled() bool { return c.Directions == DirBoth || c.Directions == DirLong }

// shortEnabled reports whether SHORT (death-cross) touches may be emitted.
func (c Config) shortEnabled() bool { return c.Directions == DirBoth || c.Directions == DirShort }
