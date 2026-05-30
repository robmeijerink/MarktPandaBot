package bybit

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

// Bybit specific structs
type BybitLiquidation struct {
	Data struct {
		Symbol string `json:"symbol"`
		Side   string `json:"side"`
		Price  string `json:"price"`
		Size   string `json:"size"`
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
		conn.WriteJSON(subMsg)
		listenBybitLiquidations(conn, aggr)
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

func listenBybitLiquidations(conn *websocket.Conn, aggr *aggregator.Aggregator) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var event BybitLiquidation
		if err := json.Unmarshal(message, &event); err != nil || event.Data.Symbol == "" {
			continue
		}
		price, _ := strconv.ParseFloat(event.Data.Price, 64)
		size, _ := strconv.ParseFloat(event.Data.Size, 64)

		volumeUSDT := size * price

		aggr.AddEvent(aggregator.LiquidationEvent{
			Exchange: "bybit",
			Symbol:   event.Data.Symbol,
			Price:    price,
			Qty:      volumeUSDT,
			Side:     event.Data.Side,
		})
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
		conn.WriteJSON(subMsg)

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
