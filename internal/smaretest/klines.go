package smaretest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Bybit v5 kline REST, used for the silent warm boot and as the fallback bar
// source when the WebSocket is not delivering. This is the module's OWN client
// (isolation rule 4: the existing bybit.FetchKlines is hardcoded to interval "5",
// so we add a 3m source here rather than modifying it).
//
// api.bytick.com is Bybit's official mirror and commonly answers when
// api.bybit.com is geo-blocked (HTTP 403) from a given host — the WebSocket feed
// keeps working in that case, only REST is blocked. bases is a var so tests can
// point it at an httptest server.
var bybitRESTBases = []string{"https://api.bybit.com", "https://api.bytick.com"}

const bybitKlineMaxLimit = 1000 // Bybit v5 per-request cap

// kline list index layout: [start, open, high, low, close, volume, turnover].
const (
	bkStart = 0
	bkOpen  = 1
	bkHigh  = 2
	bkLow   = 3
	bkClose = 4
)

type bybitKlineResp struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List [][]string `json:"list"` // newest first
	} `json:"result"`
}

// bybitInterval maps a "3m"-style timeframe to the Bybit v5 interval string
// ("3"). Minute timeframes drop the trailing "m"; anything else is passed through.
func bybitInterval(timeframe string) string {
	return strings.TrimSuffix(timeframe, "m")
}

// intervalDuration returns the timeframe as a Duration (e.g. "3m" -> 3 minutes),
// defaulting to 3 minutes if it cannot be parsed.
func intervalDuration(timeframe string) time.Duration {
	if d, err := time.ParseDuration(timeframe); err == nil && d > 0 {
		return d
	}
	return 3 * time.Minute
}

// fetchClosedBars fetches up to `limit` finalized (closed) bars ending at or
// before `endMs` (0 = latest), newest-first from the venue and returned in
// chronological order. The in-progress candle is excluded. It falls back across
// bybitRESTBases so a geo-block on one host does not kill the source.
func fetchClosedBars(client *http.Client, cfg Config, endMs int64, limit int) ([]Bar, error) {
	if limit > bybitKlineMaxLimit {
		limit = bybitKlineMaxLimit
	}
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", cfg.Symbol)
	q.Set("interval", bybitInterval(cfg.Timeframe))
	q.Set("limit", strconv.Itoa(limit))
	if endMs > 0 {
		q.Set("end", strconv.FormatInt(endMs, 10))
	}
	query := q.Encode()

	var parsed bybitKlineResp
	var lastErr error
	for _, base := range bybitRESTBases {
		p, err := requestKlines(client, base+"/v5/market/kline?"+query)
		if err != nil {
			lastErr = err
			continue // try the next host (e.g. bytick mirror on a 403)
		}
		parsed, lastErr = p, nil
		break
	}
	if lastErr != nil {
		return nil, lastErr
	}

	curBucket := time.Now().UTC().Truncate(intervalDuration(cfg.Timeframe))
	out := make([]Bar, 0, len(parsed.Result.List))
	for _, row := range parsed.Result.List {
		b, ok := parseBar(row)
		if !ok {
			continue
		}
		if !b.BucketStart.Before(curBucket) {
			continue // skip the still-open candle
		}
		out = append(out, b)
	}
	// Bybit returns newest-first; reverse into chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// warmBootFetch fetches the last cfg.WarmBootBars closed bars, paginating backward
// with the `end` parameter (do not assume one request returns them all, §3). Bars
// are returned in chronological order, de-duplicated by bucket.
func warmBootFetch(client *http.Client, cfg Config) ([]Bar, error) {
	want := cfg.WarmBootBars
	seen := make(map[int64]Bar, want)
	var endMs int64 // 0 = latest on the first page
	for len(seen) < want {
		page := want - len(seen)
		if page > bybitKlineMaxLimit {
			page = bybitKlineMaxLimit
		}
		bars, err := fetchClosedBars(client, cfg, endMs, page)
		if err != nil {
			return nil, err
		}
		if len(bars) == 0 {
			break // no more history available
		}
		oldest := bars[0].BucketStart
		newCount := 0
		for _, b := range bars {
			ms := b.BucketStart.UnixMilli()
			if _, dup := seen[ms]; !dup {
				seen[ms] = b
				newCount++
			}
		}
		if newCount == 0 {
			break // pagination made no progress; stop
		}
		// Page further back: end just before the oldest bar we have.
		endMs = oldest.UnixMilli() - 1
	}

	out := make([]Bar, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	sortBarsChrono(out)
	if len(out) > want {
		out = out[len(out)-want:]
	}
	return out, nil
}

// requestKlines performs one kline GET against a single host and decodes it.
func requestKlines(client *http.Client, endpoint string) (bybitKlineResp, error) {
	var parsed bybitKlineResp
	resp, err := client.Get(endpoint)
	if err != nil {
		return parsed, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parsed, fmt.Errorf("bybit kline HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return parsed, err
	}
	if parsed.RetCode != 0 {
		return parsed, fmt.Errorf("bybit kline retCode %d: %s", parsed.RetCode, parsed.RetMsg)
	}
	return parsed, nil
}

func parseBar(row []string) (Bar, bool) {
	if len(row) <= bkClose {
		return Bar{}, false
	}
	startMs, err := strconv.ParseInt(row[bkStart], 10, 64)
	if err != nil {
		return Bar{}, false
	}
	open, _ := strconv.ParseFloat(row[bkOpen], 64)
	high, _ := strconv.ParseFloat(row[bkHigh], 64)
	low, _ := strconv.ParseFloat(row[bkLow], 64)
	cl, err := strconv.ParseFloat(row[bkClose], 64)
	if err != nil {
		return Bar{}, false // a bad close would poison the SMA; drop the row (mirrors the WS path)
	}
	return Bar{
		BucketStart: time.UnixMilli(startMs).UTC(),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       cl,
	}, true
}

// sortBarsChrono sorts bars in place by bucket start, oldest first. The slices are
// small (<= WarmBootBars) so a simple insertion sort keeps the dependency surface
// to the standard library only.
func sortBarsChrono(bars []Bar) {
	for i := 1; i < len(bars); i++ {
		for j := i; j > 0 && bars[j].BucketStart.Before(bars[j-1].BucketStart); j-- {
			bars[j], bars[j-1] = bars[j-1], bars[j]
		}
	}
}
