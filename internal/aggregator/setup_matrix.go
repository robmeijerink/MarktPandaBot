package aggregator

import (
	"fmt"
	"strings"
)

// SetupInputs carries the values needed to score the T0 Setup Matrix. They are
// all read from what the EXISTING alert already computed (OI delta %, long/short
// liquidation split, primary-exchange funding) plus the current bucket volume vs
// the ring median. This struct is the single, explicit hand-off from the existing
// alert path into the new scoring code (D2: the existing alert is the trigger).
type SetupInputs struct {
	OIChangePct   float64 // signed OI change over the window, in % (negative == drop)
	LongLiqUSDT   float64 // combined long-liquidation notional this window
	ShortLiqUSDT  float64 // combined short-liquidation notional this window
	FundingRate   float64 // current funding on PrimaryExchange (fraction, e.g. -0.0001)
	BucketVol     float64 // most recent completed aggregated bucket quote-volume
	MedianVol     float64 // ring median
	VolMedianOK   bool    // false while the ring is still warming up
}

// SetupScore is the scored result of one T0 evaluation.
type SetupScore struct {
	OIDrop   bool
	Skew     bool
	VolSpike bool
	Funding  bool

	// VolWarming is true when Vol Spike could not be evaluated because the ring
	// has not reached MinBufferFill. It scores 0 and renders as warming-up.
	VolWarming bool

	Total int
	Max   int
}

// longLiqShare returns the long-liquidation share in percent (0–100). With no
// liquidation notional at all it returns 0 (cannot be skewed long).
func longLiqShare(longUSDT, shortUSDT float64) float64 {
	total := longUSDT + shortUSDT
	if total <= 0 {
		return 0
	}
	return longUSDT / total * 100
}

// ScoreSetup evaluates the four T0 items against the configured weights/thresholds.
//
//   - OI Drop : OIChangePct <= -OIDropPct                      => WeightOIDrop
//   - Skew    : long-liq share >= SkewPct                      => WeightSkew
//   - Vol Spike: VolMedianOK && BucketVol >= VolSpikeMult*median => WeightVolSpike
//   - Funding : FundingRate <= FundingNegThreshold             => WeightFunding
//
// Funding uses only the absolute-sign test: funding history is not tracked, so the
// FundingTrendDropPct path of §4 is unavailable. The absolute test is always
// available at alert time, so Funding is never N/A and Max stays at MaxT0Score (7).
func ScoreSetup(cfg Config, in SetupInputs) SetupScore {
	s := SetupScore{Max: cfg.MaxT0Score}

	s.OIDrop = in.OIChangePct <= -cfg.OIDropPct
	s.Skew = longLiqShare(in.LongLiqUSDT, in.ShortLiqUSDT) >= cfg.SkewPct

	if !in.VolMedianOK || in.MedianVol <= 0 {
		s.VolWarming = true
		s.VolSpike = false
	} else {
		s.VolSpike = in.BucketVol >= cfg.VolSpikeMult*in.MedianVol
	}

	s.Funding = in.FundingRate <= cfg.FundingNegThreshold

	if s.OIDrop {
		s.Total += cfg.WeightOIDrop
	}
	if s.Skew {
		s.Total += cfg.WeightSkew
	}
	if s.VolSpike {
		s.Total += cfg.WeightVolSpike
	}
	if s.Funding {
		s.Total += cfg.WeightFunding
	}
	return s
}

// QualifiesForConfirmation reports the D3 gate: confirmation starts only when the
// ABSOLUTE T0 score reaches StartConfirmationMinScore (not a ratio of Max).
func (s SetupScore) QualifiesForConfirmation(cfg Config) bool {
	return s.Total >= cfg.StartConfirmationMinScore
}

// mark renders the per-item status glyph: ✅ pass, ❌ fail, ⚪ N/A.
func mark(pass, na bool) string {
	switch {
	case na:
		return "⚪"
	case pass:
		return "✅"
	default:
		return "❌"
	}
}

// FormatSetupMatrix renders the T0 Setup Matrix block that is APPENDED to the
// bottom of the existing alert (§5). It never touches the lines above the
// separator. The "Monitoring" line is shown only for qualifying setups, since
// low-score setups deliberately get no follow-up (D3) and advertising a monitor
// that will not run would be misleading.
func FormatSetupMatrix(cfg Config, s SetupScore) string {
	conviction := "LOW"
	if s.QualifiesForConfirmation(cfg) {
		conviction = "HIGH CONVICTION"
	}

	volScore := "0"
	volMark := mark(s.VolSpike, s.VolWarming)
	if s.VolSpike {
		volScore = fmt.Sprintf("%d", cfg.WeightVolSpike)
	} else if s.VolWarming {
		volScore = "0 (warming up)"
	}

	var b strings.Builder
	b.WriteString("\n------------------------\n")
	b.WriteString("📊 SETUP MATRIX (T0)\n")
	b.WriteString(fmt.Sprintf("%s OI Drop (>=%.1f%%)   : %s\n", mark(s.OIDrop, false), cfg.OIDropPct, scoreCell(s.OIDrop, cfg.WeightOIDrop)))
	b.WriteString(fmt.Sprintf("%s Skew (>=%.0f%% long)   : %s\n", mark(s.Skew, false), cfg.SkewPct, scoreCell(s.Skew, cfg.WeightSkew)))
	b.WriteString(fmt.Sprintf("%s Vol Spike (>=%.0fx) : %s\n", volMark, cfg.VolSpikeMult, volScore))
	b.WriteString(fmt.Sprintf("%s Funding Flip               : %s\n", mark(s.Funding, false), scoreCell(s.Funding, cfg.WeightFunding)))
	b.WriteString(fmt.Sprintf("\nScore: %d/%d   (%s)\n", s.Total, s.Max, conviction))
	if s.QualifiesForConfirmation(cfg) {
		b.WriteString("⏳ Monitoring absorption window…")
	}
	return b.String()
}

// scoreCell renders an item's contributed score: its weight on pass, 0 on fail.
func scoreCell(pass bool, weight int) string {
	if pass {
		return fmt.Sprintf("%d", weight)
	}
	return "0"
}
