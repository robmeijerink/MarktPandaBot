package calendar

import "strings"

// Impact score, 1–5: how much BTC volatility an event usually brings. Bitcoin trades
// as a US-liquidity asset, so the score starts from Forex Factory's impact rating and
// weights US releases — above all inflation, jobs and the Fed — far above the rest.
//
//	5  US tier-1: Fed rate decision / statement / press conference, CPI, core PCE,
//	   non-farm payrolls (the official report, not ADP), the Fed Chair speaking
//	4  any other high-impact US release, or a US PPI / GDP / retail sales / ISM /
//	   JOLTS / jobless claims / unemployment-rate / FOMC-minutes print
//	3  medium-impact US release; a rate decision by the ECB, BoJ, BoE or PBoC
//	2  other high-impact non-US release; a US bank holiday (thin liquidity)
//	1  everything else
var (
	usTier1 = []string{
		"Federal Funds Rate", "FOMC Statement", "FOMC Press Conference", "FOMC Economic Projections",
		"CPI", "Core PCE Price Index", "Non-Farm Employment Change", "Fed Chair",
	}
	usTier2 = []string{
		"PPI", "GDP", "Retail Sales", "ISM", "JOLTS", "Unemployment Claims", "Unemployment Rate",
		"FOMC Meeting Minutes", "Average Hourly Earnings",
	}
	majorCentralBanks = map[string][]string{
		"EUR": {"Main Refinancing Rate", "ECB Press Conference", "Monetary Policy Statement"},
		"JPY": {"BOJ Policy Rate", "BOJ Press Conference", "Monetary Policy Statement"},
		"GBP": {"Official Bank Rate", "Monetary Policy Summary"},
		"CNY": {"Loan Prime Rate"},
	}
)

// score rates one event, 1–5 (0 for entries that are never worth showing).
func score(e Event) int {
	impact := strings.ToLower(e.Impact)
	if e.Country == "USD" {
		switch {
		case impact == "holiday":
			return 2
		case containsAny(e.Title, usTier1) && !strings.HasPrefix(e.Title, "ADP") && impact != "low":
			return 5
		case impact == "high", containsAny(e.Title, usTier2):
			return 4
		case impact == "medium":
			return 3
		}
		return 1
	}
	switch {
	case impact == "holiday":
		return 0
	case containsAny(e.Title, majorCentralBanks[e.Country]):
		return 3
	case impact == "high":
		return 2
	}
	return 1
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
