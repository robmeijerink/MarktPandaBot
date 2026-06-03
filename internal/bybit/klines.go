package bybit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

// Bybit v5 kline REST. The closed candle's `turnover` field is the quote/USD
// volume used for the Vol Spike baseline (same definition on the warm-boot and
// live paths, per D5/§1). Endpoint/field layout per the Bybit v5 docs; the
// per-request cap is 1000, comfortably above BufferSize (288).
const (
	bybitKlineLimit = 1000
	// kline list index layout: [start, open, high, low, close, volume, turnover].
	bkStart    = 0
	bkOpen     = 1
	bkHigh     = 2
	bkLow      = 3
	bkClose    = 4
	bkTurnover = 6
)

// bybitRESTBases are tried in order. api.bytick.com is Bybit's official mirror and
// commonly answers when api.bybit.com is geo-blocked (HTTP 403) from a given host
// — note the WebSocket feed keeps working in that case, only REST is blocked. It
// is a var so tests can point it at an httptest server.
var bybitRESTBases = []string{"https://api.bybit.com", "https://api.bytick.com"}

type bybitKlineResp struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List [][]string `json:"list"` // newest first
	} `json:"result"`
}

// FetchKlines returns up to `limit` of the most recent CLOSED 5-minute klines for
// BTCUSDT (linear), in chronological order (oldest first). The in-progress candle
// is excluded so only completed buckets enter the ring. It falls back across
// bybitRESTBases so a geo-block on one host does not kill the volume baseline.
func FetchKlines(client *http.Client, limit int) ([]aggregator.Kline, error) {
	if limit > bybitKlineLimit {
		limit = bybitKlineLimit
	}
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", "BTCUSDT")
	q.Set("interval", "5")
	q.Set("limit", strconv.Itoa(limit+1)) // +1 to absorb the in-progress candle
	query := q.Encode()

	var parsed bybitKlineResp
	var lastErr error
	for _, base := range bybitRESTBases {
		p, err := requestBybitKlines(client, base+"/v5/market/kline?"+query)
		if err != nil {
			lastErr = err
			continue // try the next host (e.g. bytick mirror on a 403)
		}
		parsed = p
		lastErr = nil
		break
	}
	if lastErr != nil {
		return nil, lastErr
	}

	currentBucket := time.Now().UTC().Truncate(5 * time.Minute)
	out := make([]aggregator.Kline, 0, len(parsed.Result.List))
	for _, row := range parsed.Result.List {
		k, ok := parseBybitKline(row)
		if !ok {
			continue
		}
		if !k.BucketStart.Before(currentBucket) {
			continue // skip the still-open candle
		}
		out = append(out, k)
	}
	// Bybit returns newest-first; reverse into chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// FetchClose returns the closing price of the BTCUSDT 5-minute candle that opened
// at bucketStart. ok is false if the candle is not present in the recent window.
func FetchClose(client *http.Client, bucketStart time.Time) (float64, bool) {
	// A small window of recent klines is enough to locate a just-closed candle.
	klines, err := FetchKlines(client, 6)
	if err != nil {
		return 0, false
	}
	want := bucketStart.UTC().Truncate(5 * time.Minute)
	for _, k := range klines {
		if k.BucketStart.Equal(want) {
			return k.Close, true
		}
	}
	return 0, false
}

// requestBybitKlines performs one kline GET against a single host and decodes it.
func requestBybitKlines(client *http.Client, endpoint string) (bybitKlineResp, error) {
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

func parseBybitKline(row []string) (aggregator.Kline, bool) {
	if len(row) <= bkTurnover {
		return aggregator.Kline{}, false
	}
	startMs, err := strconv.ParseInt(row[bkStart], 10, 64)
	if err != nil {
		return aggregator.Kline{}, false
	}
	open, _ := strconv.ParseFloat(row[bkOpen], 64)
	high, _ := strconv.ParseFloat(row[bkHigh], 64)
	low, _ := strconv.ParseFloat(row[bkLow], 64)
	cl, _ := strconv.ParseFloat(row[bkClose], 64)
	turnover, _ := strconv.ParseFloat(row[bkTurnover], 64)
	return aggregator.Kline{
		BucketStart: time.UnixMilli(startMs).UTC(),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       cl,
		QuoteVol:    turnover,
	}, true
}
