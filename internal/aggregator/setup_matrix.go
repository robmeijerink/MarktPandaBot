package aggregator

import (
	"fmt"
	"math"
	"strings"
)

// SetupInputs carries the values needed to score the T0 Setup Matrix. They are
// all read from what the EXISTING alert already computed (OI delta %, long/short
// liquidation split, primary-exchange funding) plus the current bucket volume vs
// the ring median. This struct is the single, explicit hand-off from the existing
// alert path into the new scoring code (D2: the existing alert is the trigger).
type SetupInputs struct {
	OIChangePct  float64 // signed OI change over the window, in % (negative == drop)
	LongLiqUSDT  float64 // combined long-liquidation notional this window
	ShortLiqUSDT float64 // combined short-liquidation notional this window
	FundingRate  float64 // current funding on PrimaryExchange (fraction, e.g. -0.0001)
	BucketVol    float64 // most recent completed aggregated bucket quote-volume
	MedianVol    float64 // ring median
	VolMedianOK  bool    // false while the ring is still warming up

	// Adaptive references (computed by the engine from trailing rings). Used only
	// when cfg.UseAdaptiveThresholds; each *RankOK=false falls back to the fixed bar.
	VolPctileRank    float64 // fraction of ring samples below BucketVol (0..1)
	VolRankOK        bool
	OIDropPctileRank float64 // fraction of recent windows with a smaller OI drop than this
	OIDropRankOK     bool

	// CVD absorption signal (#1). PerpCVD is the net signed perp taker notional
	// (USD) over the flush window; a reversal wants it to OPPOSE the flush.
	PerpCVD       float64
	PerpActive    bool    // false => CVD renders N/A and scores 0
	LongsDominant bool    // flush direction: longs dominant => reversal-up bias
	CVDPctileRank float64 // fraction of recent |CVD| below |PerpCVD|
	CVDRankOK     bool

	// Funding flip/trend (#3). FundingPrev is funding ~FundingLookbackHours ago.
	FundingPrev       float64
	FundingHasHistory bool
}

// SetupScore is the scored result of one T0 evaluation.
type SetupScore struct {
	OIDrop   bool
	Skew     bool
	VolSpike bool
	Funding  bool
	CVD      bool

	// VolWarming is true when Vol Spike could not be evaluated because the ring
	// has not reached MinBufferFill. It scores 0 and renders as warming-up.
	VolWarming bool
	// CVDNA is true when the perp flow stream was unavailable; CVD scores 0 and
	// renders as N/A (⚪) rather than a measured fail.
	CVDNA bool

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

// ScoreSetup evaluates the five T0 items against the configured weights/thresholds.
//
//   - OI Drop : OI fell past the bar (fixed % or adaptive percentile) => WeightOIDrop
//   - Skew    : long-liq share >= SkewPct                             => WeightSkew
//   - Vol Spike: vol past the bar (fixed ×median or adaptive percentile) => WeightVolSpike
//   - Funding : funding <= threshold OR a sharp downtrend             => WeightFunding
//   - CVD     : perp taker flow OPPOSES the flush (absorption)        => WeightCVD
//
// Adaptive gates fall back to their fixed bar until the trailing ring has warmed up,
// so the score is always defined. CVD is the only item that can be N/A (perp stream
// down); like Vol-warming it then scores 0 while Max stays at MaxT0Score.
func ScoreSetup(cfg Config, in SetupInputs) SetupScore {
	s := SetupScore{Max: cfg.MaxT0Score}

	s.OIDrop = scoreOIDrop(cfg, in)
	s.Skew = longLiqShare(in.LongLiqUSDT, in.ShortLiqUSDT) >= cfg.SkewPct

	if !in.VolMedianOK || in.MedianVol <= 0 {
		s.VolWarming = true
		s.VolSpike = false
	} else if cfg.UseAdaptiveThresholds && in.VolRankOK {
		s.VolSpike = in.VolPctileRank >= cfg.VolSpikePctile
	} else {
		s.VolSpike = in.BucketVol >= cfg.VolSpikeMult*in.MedianVol
	}

	s.Funding = scoreFunding(cfg, in)
	s.CVD, s.CVDNA = scoreCVD(cfg, in)

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
	if s.CVD {
		s.Total += cfg.WeightCVD
	}
	return s
}

// scoreOIDrop passes when OI fell hard enough: in adaptive mode the drop must rank
// in the top (1-OIDropPctile) of recent windows AND actually be a drop; otherwise
// it must clear the fixed OIDropPct bar.
func scoreOIDrop(cfg Config, in SetupInputs) bool {
	if cfg.UseAdaptiveThresholds && in.OIDropRankOK {
		return in.OIChangePct < 0 && in.OIDropPctileRank >= cfg.OIDropPctile
	}
	return in.OIChangePct <= -cfg.OIDropPct
}

// scoreFunding passes on the absolute sign test (funding <= threshold) OR, when
// FundingFlipEnabled and history exists, on a sharp downtrend from a positive level
// even while still positive — longs capitulating, anticipating the flip (§4).
func scoreFunding(cfg Config, in SetupInputs) bool {
	if in.FundingRate <= cfg.FundingNegThreshold {
		return true
	}
	if cfg.FundingFlipEnabled && in.FundingHasHistory && in.FundingPrev > 0 {
		drop := (in.FundingPrev - in.FundingRate) / in.FundingPrev
		if drop >= cfg.FundingTrendDropPct/100 {
			return true
		}
	}
	return false
}

// scoreCVD passes when perp taker flow OPPOSES the liquidation direction by a
// meaningful amount (buyers absorbing a long flush / sellers into a short squeeze).
// na is true when the perp stream was inactive, in which case it scores 0.
func scoreCVD(cfg Config, in SetupInputs) (pass, na bool) {
	if !in.PerpActive {
		return false, true
	}
	opposes := (in.LongsDominant && in.PerpCVD > 0) || (!in.LongsDominant && in.PerpCVD < 0)
	if !opposes {
		return false, false
	}
	if cfg.UseAdaptiveThresholds && in.CVDRankOK {
		return in.CVDPctileRank >= cfg.CVDPctile, false
	}
	return math.Abs(in.PerpCVD) >= cfg.CVDMinNotionalUSDT, false
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

	volScore := scoreCell(s.VolSpike, cfg.WeightVolSpike)
	if s.VolWarming {
		volScore = "0 (warming up)"
	}

	// Each row: <glyph> <label padded> <condition padded> <score>. The glyph is
	// the only emoji and is identical-width across rows, so the columns to its
	// right stay aligned inside the code fence.
	row := func(glyph, label, cond, score string) string {
		return fmt.Sprintf("%s %-10s%-13s%s\n", glyph, label, cond, score)
	}

	var b strings.Builder
	b.WriteString("\n\n```\n")
	b.WriteString("📊 SETUP MATRIX (T0)\n")
	// Condition text reflects the active mode: adaptive gates show their percentile,
	// fixed gates show the multiple/threshold.
	oiCond := fmt.Sprintf("≥%.1f%% drop", cfg.OIDropPct)
	volCond := fmt.Sprintf("≥%.0f× median", cfg.VolSpikeMult)
	if cfg.UseAdaptiveThresholds {
		oiCond = fmt.Sprintf("top %.0f%% drop", (1-cfg.OIDropPctile)*100)
		volCond = fmt.Sprintf("top %.0f%% vol", (1-cfg.VolSpikePctile)*100)
	}

	b.WriteString(row(mark(s.OIDrop, false), "OI Drop", oiCond, scoreCell(s.OIDrop, cfg.WeightOIDrop)))
	b.WriteString(row(mark(s.Skew, false), "Skew", fmt.Sprintf("≥%.0f%% long", cfg.SkewPct), scoreCell(s.Skew, cfg.WeightSkew)))
	b.WriteString(row(mark(s.VolSpike, s.VolWarming), "Vol Spike", volCond, volScore))
	b.WriteString(row(mark(s.Funding, false), "Funding", "flip/trend ≤0", scoreCell(s.Funding, cfg.WeightFunding)))
	b.WriteString(row(mark(s.CVD, s.CVDNA), "CVD", "absorb flush", scoreCell(s.CVD, cfg.WeightCVD)))
	b.WriteString("──────────────────────\n")
	b.WriteString(fmt.Sprintf("Score %d / %d   ·   %s\n", s.Total, s.Max, conviction))
	b.WriteString("```")
	if s.QualifiesForConfirmation(cfg) {
		b.WriteString("\n⏳ Monitoring absorption window…")
	}
	return b.String()
}

// scoreCell renders an item's contributed score: +weight on pass, 0 on fail.
func scoreCell(pass bool, weight int) string {
	if pass {
		return fmt.Sprintf("+%d", weight)
	}
	return "0"
}
