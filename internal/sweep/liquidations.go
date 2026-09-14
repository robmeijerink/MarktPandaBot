package sweep

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// Venues whose liquidations are summed.
const (
	venueBybit = iota
	venueOKX
	venueCount
)

var venueNames = [venueCount]string{"Bybit", "OKX"}

// okxBTCContractSize is the BTC per BTC-USDT-SWAP contract (ctVal).
const okxBTCContractSize = 0.01

// liqEvent is one liquidation, normalised across venues. long is true when a LONG
// position was liquidated (a forced sell — what a swept low flushes).
type liqEvent struct {
	at    time.Time
	venue int
	long  bool
	usd   float64
}

// liqTotals sums liquidations per venue and side over a window.
type liqTotals struct {
	long, short [venueCount]float64
}

// side returns the total for one side and its per-venue split.
func (t liqTotals) side(long bool) (total float64, perVenue [venueCount]float64) {
	perVenue = t.short
	if long {
		perVenue = t.long
	}
	for _, v := range perVenue {
		total += v
	}
	return total, perVenue
}

func (t liqTotals) all() float64 {
	l, _ := t.side(true)
	s, _ := t.side(false)
	return l + s
}

// liqStore buffers recent liquidations and tracks whether each venue's stream was
// connected, so a quiet window can be told apart from a dead feed.
type liqStore struct {
	mu     sync.Mutex
	events []liqEvent
	upFrom [venueCount]time.Time // when the venue's stream last came up; zero while down
}

const liqRetention = 2 * time.Hour

func newLiqStore() *liqStore { return &liqStore{} }

func (s *liqStore) add(events ...liqEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	if len(s.events) == 0 {
		return
	}
	cutoff := s.events[len(s.events)-1].at.Add(-liqRetention)
	i := 0
	for i < len(s.events) && s.events[i].at.Before(cutoff) {
		i++
	}
	s.events = s.events[i:]
}

// setUp records a venue's stream coming up (subscribed) or going down.
func (s *liqStore) setUp(venue int, up bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !up {
		s.upFrom[venue] = time.Time{}
	} else if s.upFrom[venue].IsZero() {
		s.upFrom[venue] = now
	}
}

// covered reports whether the venue's stream has been up continuously since `from`.
func (s *liqStore) covered(venue int, from time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.upFrom[venue].IsZero() && !s.upFrom[venue].After(from)
}

// totals sums liquidations with exchange timestamps in [from, to).
func (s *liqStore) totals(from, to time.Time) liqTotals {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t liqTotals
	for _, e := range s.events {
		if e.at.Before(from) || !e.at.Before(to) {
			continue
		}
		if e.long {
			t.long[e.venue] += e.usd
		} else {
			t.short[e.venue] += e.usd
		}
	}
	return t
}

// parseBybitLiquidations decodes the data array of Bybit's allLiquidation.{symbol}
// topic. Bybit's `S` is the side of the POSITION that was liquidated ("Buy" = a long
// was liquidated) — the opposite of OKX's order side.
func parseBybitLiquidations(data json.RawMessage) []liqEvent {
	var rows []struct {
		T    int64  `json:"T"`
		Side string `json:"S"`
		Size string `json:"v"`
		Px   string `json:"p"`
	}
	if json.Unmarshal(data, &rows) != nil {
		return nil
	}
	var out []liqEvent
	for _, r := range rows {
		size, errV := strconv.ParseFloat(r.Size, 64)
		px, errP := strconv.ParseFloat(r.Px, 64)
		if errV != nil || errP != nil || r.T == 0 || (r.Side != "Buy" && r.Side != "Sell") {
			continue
		}
		out = append(out, liqEvent{at: time.UnixMilli(r.T).UTC(), venue: venueBybit, long: r.Side == "Buy", usd: size * px})
	}
	return out
}

// parseOKXLiquidations decodes an OKX liquidation-orders push, keeping instID only.
// OKX's `side` is the liquidation ORDER side ("sell" = a long was liquidated) and `sz`
// is in contracts.
func parseOKXLiquidations(raw []byte, instID string) []liqEvent {
	var msg struct {
		Data []struct {
			InstID  string `json:"instId"`
			Details []struct {
				BkPx string `json:"bkPx"`
				Sz   string `json:"sz"`
				Side string `json:"side"`
				Ts   string `json:"ts"`
			} `json:"details"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return nil
	}
	var out []liqEvent
	for _, d := range msg.Data {
		if d.InstID != instID {
			continue
		}
		for _, det := range d.Details {
			px, errP := strconv.ParseFloat(det.BkPx, 64)
			sz, errS := strconv.ParseFloat(det.Sz, 64)
			ts, errT := strconv.ParseInt(det.Ts, 10, 64)
			if errP != nil || errS != nil || errT != nil || (det.Side != "buy" && det.Side != "sell") {
				continue
			}
			out = append(out, liqEvent{at: time.UnixMilli(ts).UTC(), venue: venueOKX, long: det.Side == "sell", usd: sz * okxBTCContractSize * px})
		}
	}
	return out
}
