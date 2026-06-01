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
// with abbreviated keys (s=symbol, S=side, p=bankruptcy price, v=size).
type BybitLiquidation struct {
	Topic string `json:"topic"`
	Data  []struct {
		Symbol string `json:"s"`
		Side   string `json:"S"`
		Price  string `json:"p"`
		Size   string `json:"v"`
	} `json:"data"`
}

type BybitTicker struct {
	Topic string `json:"topic"`
	Data  struct {
		Symbol            string `json:"symbol"`
		FundingRate       string `json:"fundingRate"`
		OpenInterestValue string `json:"openInterestValue"` // OI in USD (matches OKX oiUsd)
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

				log.Printf("[BYBIT] Liquidation received: %s %s size=%.4f price=%.2f (~%.0f $)",
					d.Symbol, d.Side, size, price, volumeUSDT)

				aggr.AddEvent(aggregator.LiquidationEvent{
					Exchange: "bybit",
					Symbol:   d.Symbol,
					Price:    price,
					Qty:      volumeUSDT,
					Side:     d.Side,
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
				if event.Data.FundingRate != "" || event.Data.OpenInterestValue != "" {
					state.Mu.Lock()
					if event.Data.FundingRate != "" {
						rate, _ := strconv.ParseFloat(event.Data.FundingRate, 64)
						state.BybitFunding = rate
					}
					if event.Data.OpenInterestValue != "" {
						oi, _ := strconv.ParseFloat(event.Data.OpenInterestValue, 64)
						state.BybitOI = oi
					}
					state.Mu.Unlock()
				}
			}
		}
		conn.Close()
		log.Println("[BYBIT] Ticker stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}
