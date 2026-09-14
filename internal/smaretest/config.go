// Package smaretest is a fully self-contained 21/200 SMA pullback/retest alert
// module. It detects the Sam Price / CryptoLifer "model" on closed 1m candles as an
// ordered sequence: a 21/200 SMA cross sets the trend, price launches an impulsive
// flagpole away from the 21 SMA, pulls back in a slower flag while the 21 catches up,
// and the flag's first touch of a 21 that is still curving in the trend direction is
// the entry. A pullback all the way to the 200 SMA invalidates the setup.
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

// Config is the single source of truth for the module. The comments give the
// locked/default values; tuning needs no code change.
type Config struct {
	PrimaryExchange string // "bybit"
	Symbol          string // "BTCUSDT" (perp)
	Timeframe       string // "1m" — the timeframe Sam Price / CryptoLifer runs this model on
	FastPeriod      int    // 21
	SlowPeriod      int    // 200
	WarmBootBars    int    // 1000 — replayed silently on startup; must be >= SlowPeriod plus room to see the last cross

	Directions string // "both" (locked) | "long" | "short"

	// Touch band around the 21 SMA (TUNABLE).
	UseATRTolerance bool    // false — if true use ATRMult*ATR, else pct band
	TouchTolPct     float64 // 0.04 — percent band (0.04 = 0.04%); a tight band suited to fast 1m bars
	ATRPeriod       int     // 14
	ATRMult         float64 // 0.25

	// Flagpole (TUNABLE). After the cross, price must launch an impulsive leg in the
	// trend direction: from its origin (the lowest low in the window, for a long) to a
	// new extreme at least MinPoleMovePct away, covered within MaxPoleBars bars — a slow
	// drift never gets there in time. At the extreme price must also stand at least
	// MinPoleExtPct away from the 21 SMA: that gap is what the flag later closes. The
	// extreme is made after the cross; the leg's origin may predate it (the rally that
	// caused the cross).
	MinPoleMovePct float64 // 0.3
	MaxPoleBars    int     // 15
	MinPoleExtPct  float64 // 0.15

	// Flag (TUNABLE): the pullback after the pole's extreme, up to and including the
	// first bar that touches the 21 SMA. That first touch decides the setup — it alerts
	// only if every rule below holds, and either way the pole is consumed (another alert
	// needs a fresh pole). A new extreme before the touch extends the pole and restarts
	// the flag; a close through the 21 SMA breaks it.
	//
	// MinSMACatchUp is what tells a flag from a V. At the pole's extreme there is a gap
	// between price and the 21 SMA; by the touch it is closed. In a flag price drifts
	// sideways and the 21 catches up to it; in a V price falls straight back onto the 21.
	// It is the share of that gap closed by the 21 moving rather than by price.
	MinFlagBars       int     // 5 — a touch sooner than this is a snap back, not a flag
	MaxFlagBars       int     // 30 — a flag that has not reached the 21 by then is stale
	MaxFlagRetrace    float64 // 0.7 — the flag may give back at most this fraction of the pole
	MaxFlagSpeedRatio float64 // 1.2 — the flag may pull back at most this multiple of the pole's speed (per bar); catches dumps the catch-up rule lets through
	MinSMACatchUp     float64 // 0.33 — the 21 SMA must close at least this share of the pole's gap to price

	// 21 SMA slope (TUNABLE): at the touch the 21 must still be curving in the trend
	// direction — rising into a long, falling into a short — not flat or rolling over.
	SlopeBars   int     // 5
	MinSlopePct float64 // 0.02 — the 21 SMA must have moved >= this % over SlopeBars bars

	// Anti-spam (TUNABLE).
	// MaxSetupsPerCross caps the alerts per 21/200 cross (0 = unlimited). Each alert
	// already needs its own pole and flag; this keeps it to the model's first setup.
	MaxSetupsPerCross int // 1
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
		WarmBootBars:         1000,
		Directions:           DirBoth,
		UseATRTolerance:      false,
		TouchTolPct:          0.04,
		ATRPeriod:            14,
		ATRMult:              0.25,
		MinPoleMovePct:       0.3,
		MaxPoleBars:          15,
		MinPoleExtPct:        0.15,
		MinFlagBars:          5,
		MaxFlagBars:          30,
		MaxFlagRetrace:       0.7,
		MaxFlagSpeedRatio:    1.2,
		MinSMACatchUp:        0.33,
		SlopeBars:            5,
		MinSlopePct:          0.02,
		MaxSetupsPerCross:    1,
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
