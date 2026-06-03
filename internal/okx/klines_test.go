package okx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okxCandleRow renders one newest-first row:
// [ts,o,h,l,c,vol,volCcy,volCcyQuote,confirm].
func okxCandleRow(ts time.Time, open, high, low, cl, vol, volCcy, volCcyQuote float64, confirm string) string {
	return fmt.Sprintf(`["%d","%g","%g","%g","%g","%g","%g","%g","%s"]`,
		ts.UnixMilli(), open, high, low, cl, vol, volCcy, volCcyQuote, confirm)
}

func TestOKXFetchKlinesParsingFilterAndOrder(t *testing.T) {
	cur := time.Now().UTC().Truncate(5 * time.Minute)
	c1 := cur.Add(-15 * time.Minute)
	c2 := cur.Add(-10 * time.Minute)
	c3 := cur.Add(-5 * time.Minute)

	// Newest-first, with the in-progress candle (confirm "0") on top. volCcyQuote
	// (index 7) is the quote/USD volume we must pick — deliberately different from
	// vol (contracts) and volCcy (base) so a wrong index would be caught.
	body := fmt.Sprintf(`{"code":"0","msg":"","data":[%s,%s,%s,%s]}`,
		okxCandleRow(cur, 9, 9, 9, 9, 1, 1, 111111, "0"), // in-progress -> dropped
		okxCandleRow(c3, 3, 4, 2, 3.5, 100, 1.0, 3500, "1"),
		okxCandleRow(c2, 2, 3, 1, 2.5, 100, 1.0, 2500, "1"),
		okxCandleRow(c1, 1, 2, 1, 1.5, 100, 1.0, 1500, "1"),
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("bar"); got != "5m" {
			t.Errorf("bar = %q, want 5m", got)
		}
		if got := r.URL.Query().Get("instId"); got != okxBTCInst {
			t.Errorf("instId = %q, want %q", got, okxBTCInst)
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	old := okxRESTBase
	okxRESTBase = srv.URL
	defer func() { okxRESTBase = old }()

	klines, err := FetchKlines(srv.Client(), 3)
	if err != nil {
		t.Fatalf("FetchKlines error: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("len = %d, want 3 (in-progress excluded)", len(klines))
	}
	// Chronological order oldest -> newest.
	if !klines[0].BucketStart.Equal(c1) || !klines[2].BucketStart.Equal(c3) {
		t.Fatalf("not chronological: %v .. %v", klines[0].BucketStart, klines[2].BucketStart)
	}
	if klines[0].QuoteVol != 1500 || klines[2].QuoteVol != 3500 {
		t.Fatalf("QuoteVol picked wrong field: %v .. %v, want 1500 .. 3500",
			klines[0].QuoteVol, klines[2].QuoteVol)
	}
	if klines[2].Close != 3.5 {
		t.Fatalf("Close = %v, want 3.5", klines[2].Close)
	}
}

func TestOKXFetchClose(t *testing.T) {
	cur := time.Now().UTC().Truncate(5 * time.Minute)
	target := cur.Add(-5 * time.Minute)
	body := fmt.Sprintf(`{"code":"0","data":[%s,%s]}`,
		okxCandleRow(cur, 9, 9, 9, 9, 1, 1, 1, "0"),
		okxCandleRow(target, 3, 4, 2, 64999.5, 100, 1.0, 3500, "1"),
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	old := okxRESTBase
	okxRESTBase = srv.URL
	defer func() { okxRESTBase = old }()

	cl, ok := FetchClose(srv.Client(), target)
	if !ok || cl != 64999.5 {
		t.Fatalf("FetchClose = %v ok=%v, want 64999.5 true", cl, ok)
	}
}

func TestOKXFetchKlinesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":"50011","msg":"rate limited"}`)
	}))
	defer srv.Close()
	old := okxRESTBase
	okxRESTBase = srv.URL
	defer func() { okxRESTBase = old }()

	if _, err := FetchKlines(srv.Client(), 3); err == nil {
		t.Fatal("expected error on non-zero OKX code")
	}
}
