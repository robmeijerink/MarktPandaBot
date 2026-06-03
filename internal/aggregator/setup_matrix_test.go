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
			wantTotal: 3, wantOI: true,
		},
		{
			name:      "only skew",
			in:        SetupInputs{OIChangePct: 0, LongLiqUSDT: 95, ShortLiqUSDT: 5, FundingRate: 0.0001, BucketVol: 1, MedianVol: 1, VolMedianOK: true},
			wantTotal: 2, wantSkew: true,
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
			wantTotal: 7, wantOI: true, wantSkew: true, wantVol: true, wantFund: true,
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
	cfg := DefaultConfig() // StartConfirmationMinScore = 5

	// Score 5 (OI 3 + Skew 2) qualifies on the absolute score even though it is
	// well under the 7/7 max (i.e. NOT a ratio gate).
	qualifying := ScoreSetup(cfg, SetupInputs{
		OIChangePct: -3, LongLiqUSDT: 95, ShortLiqUSDT: 5,
		FundingRate: 0.001, BucketVol: 1, MedianVol: 1, VolMedianOK: true,
	})
	if qualifying.Total != 5 {
		t.Fatalf("precondition: Total = %d, want 5", qualifying.Total)
	}
	if !qualifying.QualifiesForConfirmation(cfg) {
		t.Fatal("score 5 did not qualify (D3 gate broken)")
	}
	if !strings.Contains(FormatSetupMatrix(cfg, qualifying), "HIGH CONVICTION") {
		t.Fatal("qualifying setup not labelled HIGH CONVICTION")
	}

	// Score 4 (OI 3 + Vol 1) does NOT qualify, and shows no monitoring line.
	below := ScoreSetup(cfg, SetupInputs{
		OIChangePct: -3, LongLiqUSDT: 1, ShortLiqUSDT: 1,
		FundingRate: 0.001, BucketVol: 50, MedianVol: 10, VolMedianOK: true,
	})
	if below.Total != 4 {
		t.Fatalf("precondition: Total = %d, want 4", below.Total)
	}
	if below.QualifiesForConfirmation(cfg) {
		t.Fatal("score 4 qualified (D3 gate broken)")
	}
	out := FormatSetupMatrix(cfg, below)
	if !strings.Contains(out, "LOW") {
		t.Fatal("sub-gate setup not labelled LOW")
	}
	if strings.Contains(out, "Monitoring") {
		t.Fatal("sub-gate setup advertised a monitor it will not run")
	}
}
