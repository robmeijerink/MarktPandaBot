package aggregator

import (
	"strings"
	"testing"
)

func TestScoreSetupEachItem(t *testing.T) {
	cfg := DefaultConfig()

	// These cases exercise the FIXED-bar paths: no adaptive *RankOK is set, so each
	// gate falls back to its fixed threshold. CVD is N/A unless PerpActive is set.
	tests := []struct {
		name      string
		in        SetupInputs
		wantTotal int
		wantOI    bool
		wantSkew  bool
		wantVol   bool
		wantFund  bool
		wantCVD   bool
	}{
		{
			name:      "all fail",
			in:        SetupInputs{OIChangePct: 0, LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.0001, BucketVol: 1, MedianVol: 1, VolMedianOK: true},
			wantTotal: 0,
		},
		{
			name:      "only OI drop",
			in:        SetupInputs{OIChangePct: -3, LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.0001, BucketVol: 1, MedianVol: 1, VolMedianOK: true},
			wantTotal: 1, wantOI: true,
		},
		{
			name:      "only skew",
			in:        SetupInputs{OIChangePct: 0, LongLiqUSDT: 95, ShortLiqUSDT: 5, FundingRate: 0.0001, BucketVol: 1, MedianVol: 1, VolMedianOK: true},
			wantTotal: 1, wantSkew: true,
		},
		{
			name:      "only vol spike",
			in:        SetupInputs{OIChangePct: 0, LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.0001, BucketVol: 50, MedianVol: 10, VolMedianOK: true},
			wantTotal: 1, wantVol: true,
		},
		{
			name:      "only funding",
			in:        SetupInputs{OIChangePct: 0, LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: -0.00001, BucketVol: 1, MedianVol: 1, VolMedianOK: true},
			wantTotal: 1, wantFund: true,
		},
		{
			name:      "only CVD absorption (long flush, buyers step in)",
			in:        SetupInputs{OIChangePct: 0, LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.0001, BucketVol: 1, MedianVol: 1, VolMedianOK: true, PerpActive: true, LongsDominant: true, PerpCVD: 1_000_000},
			wantTotal: 1, wantCVD: true,
		},
		{
			name:      "full house = MaxT0Score",
			in:        SetupInputs{OIChangePct: -2.5, LongLiqUSDT: 90, ShortLiqUSDT: 10, FundingRate: 0, BucketVol: 50, MedianVol: 10, VolMedianOK: true, PerpActive: true, LongsDominant: true, PerpCVD: 1_000_000},
			wantTotal: 5, wantOI: true, wantSkew: true, wantVol: true, wantFund: true, wantCVD: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := ScoreSetup(cfg, tc.in)
			if s.Total != tc.wantTotal {
				t.Fatalf("Total = %d, want %d", s.Total, tc.wantTotal)
			}
			if s.Max != cfg.MaxT0Score {
				t.Fatalf("Max = %d, want fixed %d", s.Max, cfg.MaxT0Score)
			}
			if s.OIDrop != tc.wantOI || s.Skew != tc.wantSkew || s.VolSpike != tc.wantVol || s.Funding != tc.wantFund || s.CVD != tc.wantCVD {
				t.Fatalf("items = (oi %v skew %v vol %v fund %v cvd %v), want (%v %v %v %v %v)",
					s.OIDrop, s.Skew, s.VolSpike, s.Funding, s.CVD, tc.wantOI, tc.wantSkew, tc.wantVol, tc.wantFund, tc.wantCVD)
			}
		})
	}
}

// TestCVDAbsorptionDirection verifies the CVD item only passes when flow OPPOSES the
// flush, is N/A when the perp stream is down, and respects the fixed notional floor.
func TestCVDAbsorptionDirection(t *testing.T) {
	cfg := DefaultConfig() // adaptive on, but no CVDRankOK here => fixed floor path

	base := SetupInputs{LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.001, BucketVol: 1, MedianVol: 1, VolMedianOK: true}

	// Long flush + buyers absorbing (CVD > 0, big) => pass.
	in := base
	in.PerpActive, in.LongsDominant, in.PerpCVD = true, true, cfg.CVDMinNotionalUSDT+1
	if s := ScoreSetup(cfg, in); !s.CVD || s.CVDNA {
		t.Fatalf("long flush with buying absorption should pass CVD (cvd=%v na=%v)", s.CVD, s.CVDNA)
	}

	// Long flush but flow CONFIRMS the dump (CVD < 0) => fail, not N/A.
	in.PerpCVD = -1_000_000
	if s := ScoreSetup(cfg, in); s.CVD || s.CVDNA {
		t.Fatalf("flow confirming the flush must fail CVD (cvd=%v na=%v)", s.CVD, s.CVDNA)
	}

	// Absorption present but below the notional floor => fail.
	in.PerpCVD = cfg.CVDMinNotionalUSDT - 1
	if s := ScoreSetup(cfg, in); s.CVD {
		t.Fatalf("sub-floor absorption must not pass CVD")
	}

	// Short squeeze + sellers stepping in (CVD < 0, big) => pass.
	in = base
	in.PerpActive, in.LongsDominant, in.PerpCVD = true, false, -(cfg.CVDMinNotionalUSDT + 1)
	if s := ScoreSetup(cfg, in); !s.CVD {
		t.Fatalf("short squeeze with selling absorption should pass CVD")
	}

	// Perp stream inactive => N/A, scores 0.
	in.PerpActive = false
	if s := ScoreSetup(cfg, in); s.CVD || !s.CVDNA {
		t.Fatalf("inactive perp stream should be N/A (cvd=%v na=%v)", s.CVD, s.CVDNA)
	}
}

// TestAdaptiveThresholds verifies the percentile gates for OI Drop and Vol Spike
// engage when *RankOK is set and supersede the fixed bar.
func TestAdaptiveThresholds(t *testing.T) {
	cfg := DefaultConfig() // UseAdaptiveThresholds = true

	// OI drop that FAILS the fixed bar (-0.3% > -0.7%) but ranks in the top decile
	// => adaptive pass.
	in := SetupInputs{
		OIChangePct: -0.3, LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.001,
		BucketVol: 1, MedianVol: 1, VolMedianOK: true,
		OIDropPctileRank: 0.95, OIDropRankOK: true,
	}
	if s := ScoreSetup(cfg, in); !s.OIDrop {
		t.Fatal("a small drop ranking in the top decile should pass adaptive OI Drop")
	}
	// Same small drop, low rank => fail.
	in.OIDropPctileRank = 0.10
	if s := ScoreSetup(cfg, in); s.OIDrop {
		t.Fatal("a low-ranked drop should fail adaptive OI Drop")
	}

	// Vol that FAILS the fixed 5x bar (2x median) but is a top-decile reading.
	in2 := SetupInputs{
		LongLiqUSDT: 1, ShortLiqUSDT: 1, FundingRate: 0.001,
		BucketVol: 20, MedianVol: 10, VolMedianOK: true,
		VolPctileRank: 0.95, VolRankOK: true,
	}
	if s := ScoreSetup(cfg, in2); !s.VolSpike {
		t.Fatal("a top-decile volume reading should pass adaptive Vol Spike")
	}
}

// TestFundingFlipTrend verifies funding passes on a sharp downtrend from a positive
// level even while still positive, in addition to the absolute sign test.
func TestFundingFlipTrend(t *testing.T) {
	cfg := DefaultConfig() // FundingFlipEnabled, FundingTrendDropPct = 30

	// Still positive (+0.002%) but fell 80% from +0.01% an hour ago => trend pass.
	in := SetupInputs{
		LongLiqUSDT: 1, ShortLiqUSDT: 1, BucketVol: 1, MedianVol: 1, VolMedianOK: true,
		FundingRate: 0.00002, FundingPrev: 0.0001, FundingHasHistory: true,
	}
	if s := ScoreSetup(cfg, in); !s.Funding {
		t.Fatal("a sharp funding downtrend should pass Funding even while positive")
	}
	// Positive and barely changed => fail.
	in.FundingRate = 0.00009
	if s := ScoreSetup(cfg, in); s.Funding {
		t.Fatal("a flat positive funding should fail Funding")
	}
	// Absolute sign test still works with no history.
	in2 := SetupInputs{LongLiqUSDT: 1, ShortLiqUSDT: 1, BucketVol: 1, MedianVol: 1, VolMedianOK: true, FundingRate: -0.00001}
	if s := ScoreSetup(cfg, in2); !s.Funding {
		t.Fatal("negative funding should pass the absolute test")
	}
}

func TestVolSpikeWarmingScoresZero(t *testing.T) {
	cfg := DefaultConfig()
	// Even with a huge bucket vs a tiny median, a not-ready ring scores 0.
	s := ScoreSetup(cfg, SetupInputs{BucketVol: 1e9, MedianVol: 1, VolMedianOK: false})
	if s.VolSpike {
		t.Fatal("VolSpike scored on a warming ring")
	}
	if !s.VolWarming {
		t.Fatal("VolWarming not flagged")
	}
	if !strings.Contains(FormatSetupMatrix(cfg, s), "warming up") {
		t.Fatal("matrix does not show warming up")
	}
}

func TestD3GateOnAbsoluteScore(t *testing.T) {
	cfg := DefaultConfig() // StartConfirmationMinScore = 3 (3 signals agreeing, of 5)

	// 3 signals (Skew + Vol + Funding) qualifies WITHOUT OI Drop or CVD — the key
	// fix: no single signal is mandatory. Evaluated on the absolute score, not a
	// ratio of Max.
	qualifying := ScoreSetup(cfg, SetupInputs{
		OIChangePct: 0, LongLiqUSDT: 95, ShortLiqUSDT: 5, // OI fails, Skew passes
		FundingRate: -0.00001, BucketVol: 50, MedianVol: 10, VolMedianOK: true,
	})
	if qualifying.Total != 3 {
		t.Fatalf("precondition: Total = %d, want 3", qualifying.Total)
	}
	if qualifying.OIDrop {
		t.Fatal("precondition: OI Drop should be failing in this case")
	}
	if !qualifying.QualifiesForConfirmation(cfg) {
		t.Fatal("3-of-4 setup did not qualify (gate broken / OI still mandatory)")
	}
	if !strings.Contains(FormatSetupMatrix(cfg, qualifying), "HIGH CONVICTION") {
		t.Fatal("qualifying setup not labelled HIGH CONVICTION")
	}

	// Only 2 of 4 signals does NOT qualify, and shows no monitoring line.
	below := ScoreSetup(cfg, SetupInputs{
		OIChangePct: -3, LongLiqUSDT: 1, ShortLiqUSDT: 1, // OI passes, Skew fails
		FundingRate: 0.001, BucketVol: 50, MedianVol: 10, VolMedianOK: true, // Vol passes, Funding fails
	})
	if below.Total != 2 {
		t.Fatalf("precondition: Total = %d, want 2", below.Total)
	}
	if below.QualifiesForConfirmation(cfg) {
		t.Fatal("2-of-4 setup qualified (gate too loose)")
	}
	out := FormatSetupMatrix(cfg, below)
	if !strings.Contains(out, "LOW") {
		t.Fatal("sub-gate setup not labelled LOW")
	}
	if strings.Contains(out, "Monitoring") {
		t.Fatal("sub-gate setup advertised a monitor it will not run")
	}
}
