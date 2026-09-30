package calendar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNextSend(t *testing.T) {
	loc := amsterdam(t)
	cfg := DefaultConfig()
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 9, 30, 6, 59, 0, 0, loc), time.Date(2026, 9, 30, 7, 0, 0, 0, loc)},
		{time.Date(2026, 9, 30, 7, 0, 0, 0, loc), time.Date(2026, 10, 1, 7, 0, 0, 0, loc)},
		{time.Date(2026, 9, 30, 22, 0, 0, 0, loc), time.Date(2026, 10, 1, 7, 0, 0, 0, loc)},
		// DST ends on 25 Oct 2026: still 07:00 local, now UTC+1.
		{time.Date(2026, 10, 24, 12, 0, 0, 0, loc), time.Date(2026, 10, 25, 7, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := nextSend(cfg, c.now.UTC(), loc); !got.Equal(c.want) {
			t.Errorf("nextSend(%s) = %s, want %s", c.now, got, c.want)
		}
	}
	if got := nextSend(cfg, time.Date(2026, 10, 24, 12, 0, 0, 0, loc), loc); got.UTC().Hour() != 6 {
		t.Errorf("07:00 Amsterdam in winter must be 06:00 UTC, got %s", got.UTC())
	}
}

func TestScore(t *testing.T) {
	cases := []struct {
		country, title, impact string
		want                   int
	}{
		{"USD", "Core CPI m/m", "High", 5},
		{"USD", "Federal Funds Rate", "High", 5},
		{"USD", "Non-Farm Employment Change", "High", 5},
		{"USD", "Fed Chair Powell Speaks", "Medium", 5},
		{"USD", "ADP Non-Farm Employment Change", "High", 4},
		{"USD", "Unemployment Claims", "High", 4},
		{"USD", "Core PPI m/m", "Medium", 4},
		{"USD", "CB Consumer Confidence", "High", 4},
		{"USD", "Pending Home Sales m/m", "Medium", 3},
		{"USD", "FOMC Member Bowman Speaks", "Low", 1},
		{"USD", "Bank Holiday", "Holiday", 2},
		{"EUR", "Main Refinancing Rate", "High", 3},
		{"JPY", "BOJ Policy Rate", "High", 3},
		{"AUD", "Cash Rate", "High", 2},
		{"EUR", "German Prelim CPI m/m", "Medium", 1},
		{"GBP", "Bank Holiday", "Holiday", 0},
	}
	for _, c := range cases {
		if got := score(Event{Country: c.country, Title: c.title, Impact: c.impact}); got != c.want {
			t.Errorf("score(%s %q %s) = %d, want %d", c.country, c.title, c.impact, got, c.want)
		}
	}
}

const sampleFeed = `[
{"title":"Cash Rate","country":"AUD","date":"2026-09-29T00:30:00-04:00","impact":"High","forecast":"4.60%","previous":"4.35%"},
{"title":"Bank Holiday","country":"USD","date":"2026-09-30T00:00:00-04:00","impact":"Holiday","forecast":"","previous":""},
{"title":"FOMC Member Cook Speaks","country":"USD","date":"2026-09-30T08:00:00-04:00","impact":"Low","forecast":"","previous":""},
{"title":"Core PCE Price Index m/m","country":"USD","date":"2026-09-30T08:30:00-04:00","impact":"High","forecast":"0.2%","previous":"0.3%"},
{"title":"ISM_Manufacturing *PMI*","country":"USD","date":"2026-09-30T10:00:00-04:00","impact":"High","forecast":"49.5","previous":"48.7"},
{"title":"BOJ Policy Rate","country":"JPY","date":"2026-09-30T23:00:00-04:00","impact":"High","forecast":"0.75%","previous":"0.50%"},
{"title":"Non-Farm Employment Change","country":"USD","date":"2026-10-02T08:30:00-04:00","impact":"High","forecast":"120K","previous":"95K"},
{"title":"broken","country":"USD","date":"not a date","impact":"High"}
]`

func TestFetchSelectAndBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleFeed))
	}))
	defer srv.Close()

	events, err := fetchEvents(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 7 {
		t.Fatalf("want 7 parsed events (the broken date dropped), got %d", len(events))
	}

	loc := amsterdam(t)
	cfg := DefaultConfig()
	now := time.Date(2026, 9, 30, 7, 0, 0, 0, loc)
	sel := selectEvents(cfg, events, now, loc)
	var titles []string
	for _, e := range sel {
		titles = append(titles, e.Title)
	}
	want := "Bank Holiday|Core PCE Price Index m/m|ISM_Manufacturing *PMI*|BOJ Policy Rate"
	if got := strings.Join(titles, "|"); got != want {
		t.Fatalf("selected %q, want %q", got, want)
	}

	msg := buildBriefing(cfg, sel, now, loc)
	for _, s := range []string{
		"🗓 *MACRO CALENDAR* — Wed 30 Sep",
		"Next 24h · times in CEST",
		"⚡ Biggest: *5/5* at 14:30 — Core PCE Price Index m/m",
		"*14:30* 🇺🇸 Core PCE Price Index m/m\n🔴 5/5 · fcst 0.2% · prev 0.3%\n\n",
		"*16:00* 🇺🇸 ISM\\_Manufacturing \\*PMI\\*\n🟠 4/5 · fcst 49.5",
		"*Thu 05:00* 🇯🇵 BOJ Policy Rate\n🟡 3/5",
		"*All day* 🇺🇸 Bank Holiday\n⚪ 2/5 · Bank holiday — thin liquidity",
		"ℹ️ Score 1–5 = expected BTC volatility.",
	} {
		if !strings.Contains(msg, s) {
			t.Errorf("briefing missing %q:\n%s", s, msg)
		}
	}
}

func TestBuildEmpty(t *testing.T) {
	loc := amsterdam(t)
	cfg := DefaultConfig()
	now := time.Date(2026, 10, 4, 7, 0, 0, 0, loc)
	if msg := buildBriefing(cfg, nil, now, loc); msg != "" {
		t.Errorf("a day without events must send nothing, got %q", msg)
	}
}
