package binance

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

// Binance specific structs
type BinanceForceOrder struct {
	Order struct {
		Symbol string `json:"s"`
		Side   string `json:"S"`
		Price  string `json:"p"`
		Qty    string `json:"q"`
	} `json:"o"`
}

type BinanceMarkPrice struct {
	Symbol      string `json:"s"`
	FundingRate string `json:"r"`
}

func MaintainBinanceForceOrders(aggr *aggregator.Aggregator) {
	wsURL := "wss://fstream.binance.com/ws/btcusdt@forceOrder"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("Binance ForceOrder Dial Error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		listenBinanceForceOrders(conn, aggr)
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

func listenBinanceForceOrders(conn *websocket.Conn, aggr *aggregator.Aggregator) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var event BinanceForceOrder
		if err := json.Unmarshal(message, &event); err != nil {
			continue
		}
		price, _ := strconv.ParseFloat(event.Order.Price, 64)
		qty, _ := strconv.ParseFloat(event.Order.Qty, 64)

		aggr.AddEvent(aggregator.LiquidationEvent{
			Exchange: "binance",
			Symbol:   event.Order.Symbol,
			Price:    price,
			Qty:      qty,
			Side:     event.Order.Side,
		})
	}
}

func MaintainBinanceMarkPrice(state *aggregator.MarketState) {
	wsURL := "wss://fstream.binance.com/ws/btcusdt@markPrice@1s"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var event BinanceMarkPrice
			if err := json.Unmarshal(message, &event); err == nil && event.FundingRate != "" {
				rate, _ := strconv.ParseFloat(event.FundingRate, 64)
				state.Mu.Lock()
				state.BinanceFunding = rate
				state.Mu.Unlock()
			}
		}
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}
