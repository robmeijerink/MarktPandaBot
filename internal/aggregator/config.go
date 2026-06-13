package aggregator

// Config is the single source of truth for the two-stage liquidation scoring
// upgrade (see upgrade.md). Every value marked TUNABLE in the spec lives here so
// the scoring rule can be re-calibrated without code changes. None of these are
// validated edges — they are reasonable starting points that MUST be backtested
// before the T0 score / T+N verdict are trusted (see the Calibration section of
// upgrade.md).
type Config struct {
	Exchanges       []string // ["bybit","okx"]
	PrimaryExchange string   // "bybit" — used for the reclaim reference close
	BufferSize      int      // 288  (24h of 5-min buckets)
	MinBufferFill   int      // 144  — below this, Vol Spike scores 0 (still warming)

	// T0 Setup Matrix weights (TUNABLE)
	WeightOIDrop   int // 3 — requires OI drop >= OIDropPct
	WeightSkew     int // 2 — requires long-liquidation share >= SkewPct
	WeightVolSpike int // 1 — requires vol >= VolSpikeMult * median
	WeightFunding  int // 1 — requires funding sign/trend pass

	OIDropPct            float64 // 0.7
	SkewPct              float64 // 90.0
	VolSpikeMult         float64 // 5.0
	FundingNegThreshold  float64 // 0.0  — pass if current funding <= this
	FundingTrendDropPct  float64 // 30.0 — OR pass if funding fell this % over lookback
	FundingLookbackHours float64 // 1.0  — lookback for the trend test (NOT 5 min)

	MaxT0Score                int // 7 (3+2+1+1) — DISPLAY denominator; keep in sync with weights
	StartConfirmationMinScore int // 5 (TUNABLE) — D3 gate, evaluated on the ABSOLUTE score

	// Candle-sync (§6)
	MinLeadSeconds    int // 180 — if next close is sooner, target the one after
	CandleIntervalSec int // 300

	// ReclaimWatchCandles: after the early flow read, if price has not yet
	// reclaimed the flush high, keep watching this many further candles for a
	// reclaim before sending the final verdict. 0 disables the second stage.
	ReclaimWatchCandles int // 3

	// Warm boot (§4)
	KlineFetchTimeoutSec int // 10
	KlineMaxRetries      int // 3
}

// DefaultConfig returns the LOCKED defaults from upgrade.md. MaxT0Score must stay
// equal to the sum of the four weights; DefaultConfig enforces that so a weight
// tweak can never silently desync the display denominator.
func DefaultConfig() Config {
	c := Config{
		Exchanges:       []string{"bybit", "okx"},
		PrimaryExchange: "bybit",
		BufferSize:      288,
		MinBufferFill:   144,

		// Equal weights: with no backtest proving one signal is more predictive,
		// weighting them equally is the honest default. Critically it avoids a
		// "mandatory signal" — at the old 3/2/1/1, OI Drop alone could veto the
		// gate (max-without-OI was 4 < gate 5), so 3-of-4 passing setups never
		// qualified. Now the gate is a plain majority vote (see below).
		WeightOIDrop:   1,
		WeightSkew:     1,
		WeightVolSpike: 1,
		WeightFunding:  1,

		// OIDropPct aligned to MinOISignalFraction (0.7%) — the bar at which the
		// engine itself first calls a directional OI move ("Potential REVERSAL",
		// see engine.go). The earlier 1.5% (StrongOISignalFraction, "Likely") sat
		// just under the largest capitulation ever seen (~2.13%) and a 1.5% drop on
		// the ~$5.4B combined OI means ~$81M flushed in one 5-min window — never
		// observed in practice (live windows top out ~0.54%), so OI Drop could not
		// fire. With the 3-of-4 gate that silently made the other three signals
		// mandatory; at 0.7% OI Drop is achievable on a real reversal while staying
		// well above the <0.1% noise of quiet windows.
		OIDropPct:            MinOISignalFraction * 100, // 0.7% — same bar as the engine's directional-move test
		SkewPct:              90.0,
		VolSpikeMult:         5.0,
		FundingNegThreshold:  0.0,
		FundingTrendDropPct:  30.0,
		FundingLookbackHours: 1.0,

		// Gate = 3 of 4 signals (with MaxT0Score 4). The follow-up fires when a
		// majority of confirmations agree, so no single signal is required and a
		// stale/flat OI feed cannot block an otherwise-strong setup.
		StartConfirmationMinScore: 3,

		MinLeadSeconds:      180,
		CandleIntervalSec:   300,
		ReclaimWatchCandles: 3,

		KlineFetchTimeoutSec: 10,
		KlineMaxRetries:      3,
	}
	c.MaxT0Score = c.WeightOIDrop + c.WeightSkew + c.WeightVolSpike + c.WeightFunding
	return c
}
