package sweep

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Bybit v5 kline REST, used for the silent warm boot and to backfill bars the
// WebSocket missed. api.bytick.com is Bybit's official mirror and answers when
// api.bybit.com is geo-blocked (HTTP 403) from the host. bases is a var so tests can
// point it at an httptest server.
var bybitRESTBases = []string{"https://api.bybit.com", "https://api.bytick.com"}

const bybitKlineMaxLimit = 1000 // Bybit v5 per-request cap

type bybitKlineResp struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List [][]string `json:"list"` // newest first: [start, open, high, low, close, volume, turnover]
	} `json:"result"`
}

// fetchBars returns closed bars with start in [from, to), oldest first, paginating
// backward from `to`. The candle still in progress at `now` is excluded.
func fetchBars(client *http.Client, cfg Config, from, to, now time.Time) ([]Bar, error) {
	step := cfg.step()
	seen := make(map[int64]Bar)
	end := to.Add(-time.Millisecond)
	for end.After(from) {
		q := url.Values{}
		q.Set("category", "linear")
		q.Set("symbol", cfg.Symbol)
		q.Set("interval", strconv.Itoa(int(step/time.Minute)))
		q.Set("limit", strconv.Itoa(bybitKlineMaxLimit))
		q.Set("start", strconv.FormatInt(from.UnixMilli(), 10))
		q.Set("end", strconv.FormatInt(end.UnixMilli(), 10))
		resp, err := requestKlines(client, q.Encode())
		if err != nil {
			return nil, err
		}
		oldest := end
		added := 0
		for _, row := range resp.Result.List {
			b, ok := parseRESTBar(row)
			if !ok || b.Start.Before(from) || !b.Start.Before(to) || b.Start.Add(step).After(now) {
				continue
			}
			if _, dup := seen[b.Start.UnixMilli()]; !dup {
				seen[b.Start.UnixMilli()] = b
				added++
			}
			if b.Start.Before(oldest) {
				oldest = b.Start
			}
		}
		if added == 0 || len(resp.Result.List) < bybitKlineMaxLimit {
			break
		}
		end = oldest.Add(-time.Millisecond)
	}
	out := make([]Bar, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// requestKlines tries each REST host in turn, so a geo-block on one does not kill the source.
func requestKlines(client *http.Client, query string) (bybitKlineResp, error) {
	var lastErr error
	for _, base := range bybitRESTBases {
		var parsed bybitKlineResp
		resp, err := client.Get(base + "/v5/market/kline?" + query)
		if err != nil {
			lastErr = err
			continue
		}
		func() {
			defer resp.Body.Close()
			switch {
			case resp.StatusCode != http.StatusOK:
				lastErr = fmt.Errorf("bybit kline HTTP %d from %s", resp.StatusCode, base)
			case json.NewDecoder(resp.Body).Decode(&parsed) != nil:
				lastErr = fmt.Errorf("bybit kline: undecodable response from %s", base)
			case parsed.RetCode != 0:
				lastErr = fmt.Errorf("bybit kline retCode %d: %s", parsed.RetCode, parsed.RetMsg)
			default:
				lastErr = nil
			}
		}()
		if lastErr == nil {
			return parsed, nil
		}
	}
	return bybitKlineResp{}, lastErr
}

func parseRESTBar(row []string) (Bar, bool) {
	if len(row) < 6 {
		return Bar{}, false
	}
	startMs, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil {
		return Bar{}, false
	}
	vals := make([]float64, 5)
	for i := range vals {
		if vals[i], err = strconv.ParseFloat(row[i+1], 64); err != nil {
			return Bar{}, false // a bad field would poison ATR/volume baselines; drop the row
		}
	}
	return Bar{Start: time.UnixMilli(startMs).UTC(), Open: vals[0], High: vals[1], Low: vals[2], Close: vals[3], Volume: vals[4]}, true
}
