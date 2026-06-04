package aggregator

import (
	"strings"
	"testing"
)

func TestScoreSetupEachItem(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name      string
		in        SetupInputs
		wantTotal int
		wantOI    bool
		wantSkew  bool
		wantVol   bool
		wantFund  bool
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
			name:      "full house = MaxT0Score",
			in:        SetupInputs{OIChangePct: -2.5, LongLiqUSDT: 90, ShortLiqUSDT: 10, FundingRate: 0, BucketVol: 50, MedianVol: 10, VolMedianOK: true},
			wantTotal: 4, wantOI: true, wantSkew: true, wantVol: true, wantFund: true,
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
			if s.OIDrop != tc.wantOI || s.Skew != tc.wantSkew || s.VolSpike != tc.wantVol || s.Funding != tc.wantFund {
				t.Fatalf("items = (oi %v skew %v vol %v fund %v), want (%v %v %v %v)",
					s.OIDrop, s.Skew, s.VolSpike, s.Funding, tc.wantOI, tc.wantSkew, tc.wantVol, tc.wantFund)
			}
		})
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
	cfg := DefaultConfig() // StartConfirmationMinScore = 3 (3-of-4 majority)

	// 3 of 4 signals (Skew + Vol + Funding) qualifies WITHOUT OI Drop — the key
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
