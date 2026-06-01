package bybit

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

const bybitPingInterval = 18 * time.Second

// Bybit specific structs.
// The allLiquidation.{symbol} topic delivers `data` as an array of entries
// with abbreviated keys (s=symbol, S=position side, p=bankruptcy price, v=size).
// NOTE: Bybit's `S` is the POSITION side that was liquidated (S="Buy" => a LONG
// was liquidated), the opposite of OKX/Binance which report the liquidation
// ORDER side. normaliseSide() converts it to the engine's order-side convention.
type BybitLiquidation struct {
	Topic string `json:"topic"`
	Data  []struct {
		Symbol string `json:"s"`
		Side   string `json:"S"`
		Price  string `json:"p"`
		Size   string `json:"v"`
	} `json:"data"`
}

// normaliseSide maps Bybit's liquidated POSITION side to the order-side
// convention the engine uses (SELL = a long was liquidated, BUY = a short was
// liquidated), matching OKX/Binance. Bybit: S="Buy" => long liquidated => SELL.
func normaliseSide(bybitPositionSide string) string {
	switch bybitPositionSide {
	case "Buy", "buy":
		return "SELL" // a long position was liquidated
	case "Sell", "sell":
		return "BUY" // a short position was liquidated
	default:
		return bybitPositionSide
	}
}

type BybitTicker struct {
	Topic string `json:"topic"`
	Data  struct {
		Symbol            string `json:"symbol"`
		FundingRate       string `json:"fundingRate"`
		OpenInterestValue string `json:"openInterestValue"` // OI in USD (matches OKX oiUsd)
		LastPrice         string `json:"lastPrice"`
		Price24hPcnt      string `json:"price24hPcnt"` // 24h change as a fraction
		HighPrice24h      string `json:"highPrice24h"`
		LowPrice24h       string `json:"lowPrice24h"`
		Turnover24h       string `json:"turnover24h"` // 24h turnover in USD
	} `json:"data"`
}

func MaintainBybitLiquidations(aggr *aggregator.Aggregator) {
	wsURL := "wss://stream.bybit.com/v5/public/linear"
	for {
		log.Printf("[BYBIT] Connecting to Liquidation stream: %s", wsURL)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[BYBIT] Liquidation dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op":   "subscribe",
			"args": []string{"allLiquidation.BTCUSDT"},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("[BYBIT] Liquidation subscribe error: %v. Reconnecting in 5s...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[BYBIT] Liquidation stream connected. Subscribed to allLiquidation.BTCUSDT.")
		listenBybitLiquidations(conn, aggr)
		conn.Close()
		log.Println("[BYBIT] Liquidation stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func listenBybitLiquidations(conn *websocket.Conn, aggr *aggregator.Aggregator) {
	pingTicker := time.NewTicker(bybitPingInterval)
	defer pingTicker.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var event BybitLiquidation
			if err := json.Unmarshal(message, &event); err != nil || len(event.Data) == 0 {
				continue
			}
			for _, d := range event.Data {
				price, _ := strconv.ParseFloat(d.Price, 64)
				size, _ := strconv.ParseFloat(d.Size, 64)

				volumeUSDT := size * price
				side := normaliseSide(d.Side)
				dir := "long"
				if side == "BUY" {
					dir = "short"
				}

				log.Printf("[BYBIT] Liquidation received: %s %s-liq size=%.4f price=%.2f (~%.0f $)",
					d.Symbol, dir, size, price, volumeUSDT)

				aggr.AddEvent(aggregator.LiquidationEvent{
					Exchange: "bybit",
					Symbol:   d.Symbol,
					Price:    price,
					Qty:      size, // BTC; engine derives USD as Qty*Price (same as OKX)
					Side:     side,
				})
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			if err := conn.WriteJSON(map[string]string{"op": "ping"}); err != nil {
				return
			}
		}
	}
}

func MaintainBybitTickers(state *aggregator.MarketState) {
	wsURL := "wss://stream.bybit.com/v5/public/linear"
	for {
		log.Printf("[BYBIT] Connecting to Ticker (Funding/OI) stream: %s", wsURL)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[BYBIT] Ticker dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op":   "subscribe",
			"args": []string{"tickers.BTCUSDT"},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("[BYBIT] Ticker subscribe error: %v. Reconnecting in 5s...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[BYBIT] Ticker stream connected. Subscribed to tickers.BTCUSDT.")

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var event BybitTicker
			if err := json.Unmarshal(message, &event); err == nil && event.Topic != "" {
				d := event.Data
				// Bybit deltas only carry changed fields, so update each one
				// only when present.
				state.Mu.Lock()
				if d.FundingRate != "" {
					rate, _ := strconv.ParseFloat(d.FundingRate, 64)
					state.BybitFunding = rate
				}
				if d.OpenInterestValue != "" {
					oi, _ := strconv.ParseFloat(d.OpenInterestValue, 64)
					state.BybitOI = oi
				}
				if d.LastPrice != "" {
					p, _ := strconv.ParseFloat(d.LastPrice, 64)
					state.BybitLastPrice = p
				}
				if d.Price24hPcnt != "" {
					pct, _ := strconv.ParseFloat(d.Price24hPcnt, 64)
					state.BybitPct24h = pct
				}
				if d.HighPrice24h != "" {
					h, _ := strconv.ParseFloat(d.HighPrice24h, 64)
					state.BybitHigh24h = h
				}
				if d.LowPrice24h != "" {
					l, _ := strconv.ParseFloat(d.LowPrice24h, 64)
					state.BybitLow24h = l
				}
				if d.Turnover24h != "" {
					to, _ := strconv.ParseFloat(d.Turnover24h, 64)
					state.BybitTurnover24h = to
				}
				state.Mu.Unlock()
			}
		}
		conn.Close()
		log.Println("[BYBIT] Ticker stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}
