package smaretest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// klineHistory is a fake Bybit kline backend serving from a fixed bar history,
// honoring the `end` pagination cursor and capping each response to `pageCap` so
// the client must paginate.
type klineHistory struct {
	bars    []Bar // chronological
	pageCap int
}

func (h *klineHistory) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		// Collect candidates with start <= end (or all if end == 0), newest first.
		var cand []Bar
		for _, b := range h.bars {
			if end == 0 || b.BucketStart.UnixMilli() <= end {
				cand = append(cand, b)
			}
		}
		sort.Slice(cand, func(i, j int) bool { return cand[i].BucketStart.After(cand[j].BucketStart) })
		if len(cand) > h.pageCap {
			cand = cand[:h.pageCap]
		}
		var rows []string
		for _, b := range cand {
			rows = append(rows, fmt.Sprintf(`["%d","%.1f","%.1f","%.1f","%.1f","0","0"]`,
				b.BucketStart.UnixMilli(), b.Open, b.High, b.Low, b.Close))
		}
		fmt.Fprintf(w, `{"retCode":0,"retMsg":"OK","result":{"list":[%s]}}`, strings.Join(rows, ","))
	}
}

func makeHistory(n int) []Bar {
	// Newest closed bar is well in the past so nothing is filtered as in-progress.
	newest := time.Now().UTC().Truncate(3 * time.Minute).Add(-1 * time.Hour)
	bars := make([]Bar, n)
	for i := 0; i < n; i++ {
		start := newest.Add(time.Duration(-(n - 1 - i)) * 3 * time.Minute)
		c := 100.0 + float64(i)
		bars[i] = Bar{BucketStart: start, Open: c, High: c + 1, Low: c - 1, Close: c}
	}
	return bars
}

func TestFetchClosedBarsOrderAndInProgress(t *testing.T) {
	hist := makeHistory(4)
	// Append an in-progress bar at the current bucket; fetchClosedBars must drop it.
	cur := time.Now().UTC().Truncate(3 * time.Minute)
	hist = append(hist, Bar{BucketStart: cur, Open: 999, High: 999, Low: 999, Close: 999})

	srv := httptest.NewServer((&klineHistory{bars: hist, pageCap: 100}).handler())
	defer srv.Close()
	defer setBases(srv.URL)()

	cfg := DefaultConfig()
	got, err := fetchClosedBars(httpTestClient(), cfg, 0, 10)
	if err != nil {
		t.Fatalf("fetchClosedBars error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 closed bars (in-progress dropped), got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !got[i].BucketStart.After(got[i-1].BucketStart) {
			t.Fatalf("bars not in chronological order at %d", i)
		}
	}
}

func TestBytickFallback(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // simulate geo-block on the primary host
	}))
	defer bad.Close()
	good := httptest.NewServer((&klineHistory{bars: makeHistory(3), pageCap: 100}).handler())
	defer good.Close()

	old := bybitRESTBases
	bybitRESTBases = []string{bad.URL, good.URL}
	defer func() { bybitRESTBases = old }()

	got, err := fetchClosedBars(httpTestClient(), DefaultConfig(), 0, 10)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 bars from the mirror, got %d", len(got))
	}
}

func TestWarmBootFetchPaginates(t *testing.T) {
	hist := makeHistory(6)
	srv := httptest.NewServer((&klineHistory{bars: hist, pageCap: 2}).handler()) // forces paging
	defer srv.Close()
	defer setBases(srv.URL)()

	cfg := DefaultConfig()
	cfg.WarmBootBars = 5
	got, err := warmBootFetch(httpTestClient(), cfg)
	if err != nil {
		t.Fatalf("warmBootFetch error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 paginated bars, got %d", len(got))
	}
	// Chronological, de-duplicated, and the most recent 5 of 6.
	for i := 1; i < len(got); i++ {
		if !got[i].BucketStart.After(got[i-1].BucketStart) {
			t.Fatalf("paginated bars not ordered/unique at %d", i)
		}
	}
	if !got[len(got)-1].BucketStart.Equal(hist[len(hist)-1].BucketStart) {
		t.Fatalf("warm boot should keep the most recent bar")
	}
}

func httpTestClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

// setBases points bybitRESTBases at a single test server and returns a restore func.
func setBases(url string) func() {
	old := bybitRESTBases
	bybitRESTBases = []string{url}
	return func() { bybitRESTBases = old }
}
