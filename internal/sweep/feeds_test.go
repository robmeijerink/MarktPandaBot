package sweep

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Bybit reports the liquidated POSITION side: "Buy" means a long was liquidated.
func TestParseBybitLiquidations(t *testing.T) {
	data := json.RawMessage(`[
		{"T": 1757844000123, "s": "BTCUSDT", "S": "Buy", "v": "2.5", "p": "60000"},
		{"T": 1757844000456, "s": "BTCUSDT", "S": "Sell", "v": "0.1", "p": "61000"},
		{"T": 1757844000789, "s": "BTCUSDT", "S": "Buy", "v": "oops", "p": "60000"}
	]`)
	got := parseBybitLiquidations(data)
	if len(got) != 2 {
		t.Fatalf("want 2 valid events (bad size dropped), got %d", len(got))
	}
	if !got[0].long || got[0].usd != 150000 || got[0].venue != venueBybit || got[0].at.UnixMilli() != 1757844000123 {
		t.Errorf("S=Buy should be a $150k LONG liquidation on Bybit at T, got %+v", got[0])
	}
	if got[1].long || got[1].usd != 6100 {
		t.Errorf("S=Sell should be a $6.1k SHORT liquidation, got %+v", got[1])
	}
}

// OKX reports the liquidation ORDER side ("sell" = a long was liquidated), size in
// 0.01 BTC contracts, and pushes every SWAP instrument, so others must be filtered out.
func TestParseOKXLiquidations(t *testing.T) {
	raw := []byte(`{"arg":{"channel":"liquidation-orders","instType":"SWAP"},"data":[
		{"instId":"BTC-USDT-SWAP","details":[
			{"bkPx":"60000","sz":"250","side":"sell","posSide":"long","ts":"1757844000000"},
			{"bkPx":"61000","sz":"10","side":"buy","posSide":"short","ts":"1757844001000"}]},
		{"instId":"ETH-USDT-SWAP","details":[{"bkPx":"3000","sz":"999","side":"sell","ts":"1757844000000"}]}
	]}`)
	got := parseOKXLiquidations(raw, "BTC-USDT-SWAP")
	if len(got) != 2 {
		t.Fatalf("want the 2 BTC events only, got %d", len(got))
	}
	if !got[0].long || got[0].usd != 150000 || got[0].venue != venueOKX {
		t.Errorf("side=sell, 250 contracts @60000 should be a $150k LONG liquidation, got %+v", got[0])
	}
	if got[1].long || got[1].usd != 6100 {
		t.Errorf("side=buy should be a $6.1k SHORT liquidation, got %+v", got[1])
	}
}

func TestLiqStoreWindowAndCoverage(t *testing.T) {
	s := newLiqStore()
	s.add(
		liqEvent{at: monday.Add(-time.Second), venue: venueBybit, long: true, usd: 1},
		liqEvent{at: monday, venue: venueBybit, long: true, usd: 10},
		liqEvent{at: monday.Add(4 * time.Minute), venue: venueOKX, long: false, usd: 5},
		liqEvent{at: monday.Add(5 * time.Minute), venue: venueBybit, long: true, usd: 100},
	)
	tot := s.totals(monday, monday.Add(5*time.Minute))
	if l, per := tot.side(true); l != 10 || per[venueBybit] != 10 {
		t.Errorf("window is [from, to): want longs 10, got %v %v", l, per)
	}
	if sh, _ := tot.side(false); sh != 5 || tot.all() != 15 {
		t.Errorf("want shorts 5 and total 15, got %v %v", sh, tot.all())
	}

	if s.covered(venueBybit, monday) {
		t.Errorf("a venue that never came up is not covered")
	}
	s.setUp(venueBybit, true, monday)
	s.setUp(venueBybit, true, monday.Add(time.Minute)) // a repeated ack keeps the original start
	if !s.covered(venueBybit, monday) || s.covered(venueBybit, monday.Add(-time.Second)) {
		t.Errorf("covered only from when the stream came up")
	}
	s.setUp(venueBybit, false, monday.Add(2*time.Minute))
	if s.covered(venueBybit, monday) {
		t.Errorf("a dropped stream is not covered")
	}

	s.add(liqEvent{at: monday.Add(3 * time.Hour), venue: venueBybit, long: true, usd: 1})
	if len(s.events) != 1 {
		t.Errorf("events older than %s before the newest must be dropped, have %d", liqRetention, len(s.events))
	}
}

// Only closed candles leave the kline parser, volume included.
func TestParseBybitKlines(t *testing.T) {
	data := json.RawMessage(`[
		{"start":1757844000000,"open":"60000","high":"60100","low":"59900","close":"60050","volume":"123.4","confirm":true},
		{"start":1757844300000,"open":"60050","high":"60060","low":"60000","close":"60010","volume":"1","confirm":false}
	]`)
	got := parseBybitKlines(data)
	want := Bar{Start: time.UnixMilli(1757844000000).UTC(), Open: 60000, High: 60100, Low: 59900, Close: 60050, Volume: 123.4}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("want only the confirmed candle %+v, got %+v", want, got)
	}
}

// When api.bybit.com is geo-blocked (403) the REST client falls back to the mirror,
// and fetchBars returns only closed candles inside [from, to), oldest first.
func TestFetchBarsFallsBackToMirror(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer blocked.Close()
	start := monday.UnixMilli()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("interval") != "5" {
			t.Errorf("interval should be 5, got %q", r.URL.Query().Get("interval"))
		}
		var rows []string
		for i := 3; i >= 0; i-- { // newest first, like Bybit
			rows = append(rows, fmt.Sprintf(`["%d","1","2","0.5","1.5","7","0"]`, start+int64(i)*300000))
		}
		fmt.Fprintf(w, `{"retCode":0,"result":{"list":[%s]}}`, strings.Join(rows, ","))
	}))
	defer mirror.Close()

	saved := bybitRESTBases
	bybitRESTBases = []string{blocked.URL, mirror.URL}
	defer func() { bybitRESTBases = saved }()

	cfg := DefaultConfig()
	now := monday.Add(17 * time.Minute) // the candle starting at +15m is still open
	bars, err := fetchBars(http.DefaultClient, cfg, monday.Add(5*time.Minute), monday.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("fallback should succeed: %v", err)
	}
	if len(bars) != 2 || !bars[0].Start.Equal(monday.Add(5*time.Minute)) || !bars[1].Start.Equal(monday.Add(10*time.Minute)) || bars[0].Volume != 7 {
		t.Fatalf("want the closed candles at +5m and +10m, oldest first, got %+v", bars)
	}
}
