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
		log.Printf("[BINANCE] Connecting to ForceOrder stream: %s", wsURL)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[BINANCE] ForceOrder dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[BINANCE] ForceOrder stream connected. Listening for liquidations...")
		listenBinanceForceOrders(conn, aggr)
		conn.Close()
		log.Println("[BINANCE] ForceOrder stream disconnected. Reconnecting in 5s...")
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

		log.Printf("[BINANCE] Liquidation received: %s %s qty=%.4f ₿ price=%.2f (~%.0f $)",
			event.Order.Symbol, event.Order.Side, qty, price, qty*price)

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
		log.Printf("[BINANCE] Connecting to MarkPrice/Funding stream: %s", wsURL)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[BINANCE] MarkPrice dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[BINANCE] MarkPrice/Funding stream connected.")
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
		log.Println("[BINANCE] MarkPrice/Funding stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}
