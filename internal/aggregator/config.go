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

	OIDropPct            float64 // 2.5
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

		WeightOIDrop:   3,
		WeightSkew:     2,
		WeightVolSpike: 1,
		WeightFunding:  1,

		OIDropPct:            2.5,
		SkewPct:              90.0,
		VolSpikeMult:         5.0,
		FundingNegThreshold:  0.0,
		FundingTrendDropPct:  30.0,
		FundingLookbackHours: 1.0,

		StartConfirmationMinScore: 5,

		MinLeadSeconds:    180,
		CandleIntervalSec: 300,

		KlineFetchTimeoutSec: 10,
		KlineMaxRetries:      3,
	}
	c.MaxT0Score = c.WeightOIDrop + c.WeightSkew + c.WeightVolSpike + c.WeightFunding
	return c
}
