// Package smaretest is a fully self-contained 21/200 SMA pullback/retest alert
// module. After a 21/200 SMA cross sets the trend on closed 1m candles, it waits
// for price to MOVE AWAY from the lines (separation) and then tighten into a
// contracting range, then takes a bar-close touch of the 21 SMA (dynamic support
// for longs / resistance for shorts) as the entry confirmation; a pullback all
// the way to the 200 SMA invalidates the setup.
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
// locked/default values; tuning needs no code change.
type Config struct {
	PrimaryExchange string // "bybit"
	Symbol          string // "BTCUSDT" (perp)
	Timeframe       string // "1m" — the timeframe Sam Price / CryptoLifer runs this model on
	FastPeriod      int    // 21
	SlowPeriod      int    // 200
	WarmBootBars    int    // 400 — must be >= SlowPeriod plus headroom (1m crosses are frequent)

	Directions string // "both" (locked) | "long" | "short"

	// Touch band around the 21 SMA (TUNABLE).
	UseATRTolerance bool    // false — if true use ATRMult*ATR, else pct band
	TouchTolPct     float64 // 0.04 — percent band (0.04 = 0.04%); a tight band suited to fast 1m bars
	ATRPeriod       int     // 14
	ATRMult         float64 // 0.25

	// Flagpole gate (TUNABLE). Sam Price's "model" enters on a pullback that KISSES
	// the 21 SMA, but only after a real flagpole: a sudden, aggressive move in the
	// trend direction that overextends price away from the 21 and leaves a gap. These
	// two knobs encode that pole. MinSeparationPct is how far price must have reached
	// from the 21 SMA (in the trend direction) — the depth of the gap. PoleWindow is
	// how RECENTLY that overextension must have happened: the peak must fall inside the
	// last PoleWindow bars, which also stands in for "aggressive" (covering the gap
	// within a short window IS an impulse; a slow drift never reaches the depth in
	// time). The kiss bar itself is excluded, so the pole is always a PRIOR move and
	// the touch proves the return. Disable with RequirePole=false.
	RequirePole      bool    // true — require a recent flagpole before the kiss
	MinSeparationPct float64 // 0.2 — the flagpole must have reached >= this % away from the 21 SMA
	PoleWindow       int     // 20 — bars; the overextension must have peaked within this many recent bars

	// Tight-flag / pennant gate (ON by default). A kiss only fires once the pullback
	// has formed a real flag: a tight, contracting range in the bars just before the
	// touch. This is what stops the alert firing on the very FIRST poke at the 21 after
	// the pole (a sharp micro-V with no consolidation) — it waits for price to settle
	// into a flag first. Turn it off to also take those immediate V-shape kisses.
	RequireTightFlag     bool    // true — require a tight, contracting range (a flag) into the touch
	FlagLookback         int     // 12 — bars (ending just before the touch bar) that form the range
	FlagMaxRangePct      float64 // 0.3 — recent-half range height must be <= this % of price (only used when RequireTightFlag)
	FlagContractionRatio float64 // 0.8 — recent-half range <= ratio*earlier-half range (only used when RequireTightFlag)

	// Re-arm / anti-spam (TUNABLE).
	ReArmMode string // "debounce" (default) | "firstOnly"
	// CooldownMin silences a second retest alert in the SAME direction within this
	// many minutes of the last one (0 = off). LONG and SHORT keep independent timers,
	// so a regime flip can still alert immediately. It applies only to this module's
	// alerts; no other notification is affected.
	CooldownMin      int  // 15
	EmitInvalidation bool // false — send a note when price reaches the 200 SMA

	BarCloseGraceSec int // 3 — wait after candle close before reading the finalized kline (1m settles fast)

	// Outcome logging (measurement, not part of the signal). For each fired retest the
	// module logs the entry feature vector and the realised forward return at these
	// horizons, so the 1m signal's real hit-rate can be measured from the logs instead
	// of eyeballed. Empty disables it.
	OutcomeHorizonsMin []int // {15, 30, 60} — forward-return horizons in minutes

	// Operational knobs for the kline source (not part of the signal).
	KlineFetchTimeoutSec int // 10 — REST HTTP client timeout
	KlineMaxRetries      int // 3  — warm-boot fetch retries before giving up
}

// DefaultConfig returns the locked/default configuration.
func DefaultConfig() Config {
	return Config{
		PrimaryExchange:      "bybit",
		Symbol:               "BTCUSDT",
		Timeframe:            "1m",
		FastPeriod:           21,
		SlowPeriod:           200,
		WarmBootBars:         400,
		Directions:           DirBoth,
		UseATRTolerance:      false,
		TouchTolPct:          0.04,
		ATRPeriod:            14,
		ATRMult:              0.25,
		RequirePole:          true,
		MinSeparationPct:     0.2,
		PoleWindow:           20,
		RequireTightFlag:     true,
		FlagLookback:         12,
		FlagMaxRangePct:      0.3,
		FlagContractionRatio: 0.8,
		ReArmMode:            ReArmDebounce,
		CooldownMin:          15,
		EmitInvalidation:     false,
		BarCloseGraceSec:     3,
		OutcomeHorizonsMin:   []int{15, 30, 60},
		KlineFetchTimeoutSec: 10,
		KlineMaxRetries:      3,
	}
}

// longEnabled reports whether LONG (golden-cross) touches may be emitted.
func (c Config) longEnabled() bool { return c.Directions == DirBoth || c.Directions == DirLong }

// shortEnabled reports whether SHORT (death-cross) touches may be emitted.
func (c Config) shortEnabled() bool { return c.Directions == DirBoth || c.Directions == DirShort }
