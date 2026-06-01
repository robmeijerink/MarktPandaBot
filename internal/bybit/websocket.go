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
		Symbol       string `json:"symbol"`
		FundingRate  string `json:"fundingRate"`
		OpenInterest string `json:"openInterest"`
	} `json:"data"`
}

func MaintainBybitLiquidations(aggr *aggregator.Aggregator) {
	wsURL := "wss://stream.bybit.com/v5/public/linear"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("Bybit Liquidation Dial Error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op":   "subscribe",
			"args": []string{"allLiquidation.BTCUSDT"},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("Bybit Liquidation subscribe error: %v", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		listenBybitLiquidations(conn, aggr)
		conn.Close()
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
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op":   "subscribe",
			"args": []string{"tickers.BTCUSDT"},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("Bybit Ticker subscribe error: %v", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var event BybitTicker
			if err := json.Unmarshal(message, &event); err == nil && event.Topic != "" {
				if event.Data.FundingRate != "" || event.Data.OpenInterest != "" {
					state.Mu.Lock()
					if event.Data.FundingRate != "" {
						rate, _ := strconv.ParseFloat(event.Data.FundingRate, 64)
						state.BybitFunding = rate
					}
					if event.Data.OpenInterest != "" {
						oi, _ := strconv.ParseFloat(event.Data.OpenInterest, 64)
						state.BybitOI = oi
					}
					state.Mu.Unlock()
				}
			}
		}
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}
