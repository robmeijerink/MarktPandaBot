package okx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

// OKX v5 candle REST. Each row is
// [ts, o, h, l, c, vol, volCcy, volCcyQuote, confirm]; volCcyQuote is the
// quote/USD volume (the field comparable to Bybit's turnover, per D5/§1) and
// confirm=="1" marks a closed candle. The /market/candles endpoint serves recent
// candles and caps at 300 per request — well under the 1000 Bybit allows — so we
// page with the `after` cursor to gather BufferSize buckets (§3).
const (
	okxCandleLimit  = 300 // OKX per-request cap for /market/candles
	ocTs            = 0
	ocOpen          = 1
	ocHigh          = 2
	ocLow           = 3
	ocClose         = 4
	ocVolCcyQuote   = 7
	ocConfirm       = 8
	okxCandleFields = 9
)

// okxRESTBase is a var (not const) so tests can point it at an httptest server.
var okxRESTBase = "https://www.okx.com"

type okxCandleResp struct {
	Code string     `json:"code"`
	Msg  string     `json:"msg"`
	Data [][]string `json:"data"` // newest first
}

// FetchKlines returns up to `limit` of the most recent CLOSED 5-minute candles
// for BTC-USDT-SWAP, in chronological order (oldest first). It pages with the
// `after` cursor because OKX caps each request at okxCandleLimit. In-progress
// candles (confirm != "1") are excluded.
func FetchKlines(client *http.Client, limit int) ([]aggregator.Kline, error) {
	var collected []aggregator.Kline
	var after string // ms timestamp cursor; empty == most recent
	firstPage := true

	for len(collected) < limit {
		// Request only what is still needed. The most recent page also carries the
		// in-progress candle (filtered out by parseOKXCandle), so pad the first
		// page by one to still net `limit` closed candles without over-fetching on
		// small requests (e.g. the live poll asking for 3).
		need := limit - len(collected)
		pageLimit := need
		if firstPage {
			pageLimit = need + 1
		}
		if pageLimit > okxCandleLimit {
			pageLimit = okxCandleLimit
		}

		batch, err := fetchOKXCandlePage(client, after, pageLimit)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break // no more history available
		}
		// batch is newest-first; the last element is the oldest in this page.
		oldest := batch[len(batch)-1]
		after = strconv.FormatInt(oldest.BucketStart.UnixMilli(), 10)
		collected = append(collected, batch...)

		// End-of-history: the server returned fewer closed candles than the page
		// could hold (the first page also gives up one slot to the in-progress
		// candle we filtered out).
		maxClosed := pageLimit
		if firstPage {
			maxClosed = pageLimit - 1
		}
		if len(batch) < maxClosed {
			break
		}
		firstPage = false
	}

	// collected is newest-first across pages; reverse to chronological order and
	// trim to the requested count (keeping the most recent `limit`).
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	if len(collected) > limit {
		collected = collected[len(collected)-limit:]
	}
	return collected, nil
}

// FetchClose returns the closing price of the BTC-USDT-SWAP 5-minute candle that
// opened at bucketStart, or ok=false if it is not in the recent window.
func FetchClose(client *http.Client, bucketStart time.Time) (float64, bool) {
	// The just-closed target candle is among the most recent; a small page covers
	// it without pulling the full 300.
	batch, err := fetchOKXCandlePage(client, "", 12)
	if err != nil {
		return 0, false
	}
	want := bucketStart.UTC().Truncate(5 * time.Minute)
	for _, k := range batch {
		if k.BucketStart.Equal(want) {
			return k.Close, true
		}
	}
	return 0, false
}

func fetchOKXCandlePage(client *http.Client, after string, limit int) ([]aggregator.Kline, error) {
	q := url.Values{}
	q.Set("instId", okxBTCInst)
	q.Set("bar", "5m")
	q.Set("limit", strconv.Itoa(limit))
	if after != "" {
		q.Set("after", after)
	}

	endpoint := okxRESTBase + "/api/v5/market/candles?" + q.Encode()
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx candle HTTP %d", resp.StatusCode)
	}

	var parsed okxCandleResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if parsed.Code != "0" {
		return nil, fmt.Errorf("okx candle code %s: %s", parsed.Code, parsed.Msg)
	}

	out := make([]aggregator.Kline, 0, len(parsed.Data))
	for _, row := range parsed.Data {
		k, ok := parseOKXCandle(row)
		if !ok {
			continue // skips malformed rows and the in-progress (confirm!="1") candle
		}
		out = append(out, k)
	}
	return out, nil
}

func parseOKXCandle(row []string) (aggregator.Kline, bool) {
	if len(row) < okxCandleFields {
		return aggregator.Kline{}, false
	}
	if row[ocConfirm] != "1" {
		return aggregator.Kline{}, false // in-progress candle
	}
	tsMs, err := strconv.ParseInt(row[ocTs], 10, 64)
	if err != nil {
		return aggregator.Kline{}, false
	}
	open, _ := strconv.ParseFloat(row[ocOpen], 64)
	high, _ := strconv.ParseFloat(row[ocHigh], 64)
	low, _ := strconv.ParseFloat(row[ocLow], 64)
	cl, _ := strconv.ParseFloat(row[ocClose], 64)
	quoteVol, _ := strconv.ParseFloat(row[ocVolCcyQuote], 64)
	return aggregator.Kline{
		BucketStart: time.UnixMilli(tsMs).UTC(),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       cl,
		QuoteVol:    quoteVol,
	}, true
}
