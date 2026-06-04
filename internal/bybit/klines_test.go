package bybit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// bybitKlineRow renders one newest-first list row: [start,open,high,low,close,volume,turnover].
func bybitKlineRow(start time.Time, open, high, low, cl, vol, turnover float64) string {
	return fmt.Sprintf(`["%d","%g","%g","%g","%g","%g","%g"]`,
		start.UnixMilli(), open, high, low, cl, vol, turnover)
}

func TestBybitFetchKlinesParsingAndOrdering(t *testing.T) {
	cur := time.Now().UTC().Truncate(5 * time.Minute)
	closedOld := cur.Add(-10 * time.Minute)
	closedNew := cur.Add(-5 * time.Minute)

	// Newest-first, with the in-progress candle (start == cur) at the top.
	body := fmt.Sprintf(`{"retCode":0,"retMsg":"OK","result":{"list":[%s,%s,%s]}}`,
		bybitKlineRow(cur, 7, 7, 7, 7, 1, 999999),        // in-progress -> must be dropped
		bybitKlineRow(closedNew, 2, 3, 1, 2.5, 10, 2500), // turnover (quote) = 2500
		bybitKlineRow(closedOld, 1, 2, 1, 1.5, 5, 1500),
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("category"); got != "linear" {
			t.Errorf("category = %q, want linear", got)
		}
		if got := r.URL.Query().Get("interval"); got != "5" {
			t.Errorf("interval = %q, want 5", got)
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	old := bybitRESTBases
	bybitRESTBases = []string{srv.URL}
	defer func() { bybitRESTBases = old }()

	klines, err := FetchKlines(srv.Client(), 100)
	if err != nil {
		t.Fatalf("FetchKlines error: %v", err)
	}
	if len(klines) != 2 {
		t.Fatalf("len = %d, want 2 (in-progress candle excluded)", len(klines))
	}
	// Chronological order: oldest first.
	if !klines[0].BucketStart.Equal(closedOld) || !klines[1].BucketStart.Equal(closedNew) {
		t.Fatalf("not chronological: %v then %v", klines[0].BucketStart, klines[1].BucketStart)
	}
	// QuoteVol must be the turnover field, not base volume.
	if klines[1].QuoteVol != 2500 {
		t.Fatalf("QuoteVol = %v, want 2500 (turnover)", klines[1].QuoteVol)
	}
	if klines[1].Close != 2.5 {
		t.Fatalf("Close = %v, want 2.5", klines[1].Close)
	}
}

func TestBybitFetchClose(t *testing.T) {
	cur := time.Now().UTC().Truncate(5 * time.Minute)
	target := cur.Add(-5 * time.Minute)
	body := fmt.Sprintf(`{"retCode":0,"result":{"list":[%s,%s]}}`,
		bybitKlineRow(cur, 7, 7, 7, 7, 1, 1),
		bybitKlineRow(target, 2, 3, 1, 64321.5, 10, 2500),
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	old := bybitRESTBases
	bybitRESTBases = []string{srv.URL}
	defer func() { bybitRESTBases = old }()

	cl, ok := FetchClose(srv.Client(), target)
	if !ok || cl != 64321.5 {
		t.Fatalf("FetchClose = %v ok=%v, want 64321.5 true", cl, ok)
	}
	if _, ok := FetchClose(srv.Client(), target.Add(-time.Hour)); ok {
		t.Fatal("FetchClose ok=true for absent bucket")
	}
}

func TestBybitFetchKlinesHTTPError(t *testing.T) {
	bybitREST.reset()
	defer bybitREST.reset()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	old := bybitRESTBases
	bybitRESTBases = []string{srv.URL}
	defer func() { bybitRESTBases = old }()

	if _, err := FetchKlines(srv.Client(), 10); err == nil {
		t.Fatal("expected error on HTTP 429")
	}
}

func TestBybitFetchKlinesFallsBackToMirror(t *testing.T) {
	cur := time.Now().UTC().Truncate(5 * time.Minute)
	closed := cur.Add(-5 * time.Minute)

	// First host is geo-blocked (403); the mirror serves a valid response.
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer blocked.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"retCode":0,"result":{"list":[%s,%s]}}`,
			bybitKlineRow(cur, 7, 7, 7, 7, 1, 1),
			bybitKlineRow(closed, 2, 3, 1, 2.5, 10, 2500))
	}))
	defer mirror.Close()

	old := bybitRESTBases
	bybitRESTBases = []string{blocked.URL, mirror.URL}
	defer func() { bybitRESTBases = old }()

	klines, err := FetchKlines(mirror.Client(), 5)
	if err != nil {
		t.Fatalf("expected mirror fallback to succeed, got: %v", err)
	}
	if len(klines) != 1 || klines[0].QuoteVol != 2500 {
		t.Fatalf("fallback returned %d klines (want 1, QuoteVol 2500)", len(klines))
	}
}
