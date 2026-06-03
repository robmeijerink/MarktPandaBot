package bybit

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

const (
	bybitLinearWsURL = "wss://stream.bybit.com/v5/public/linear"
	bybitSpotWsURL   = "wss://stream.bybit.com/v5/public/spot"
)

// bybitPublicTrade is the publicTrade.{symbol} payload. `S` is the TAKER side
// (the aggressor): "Buy" = taker bought (lifted the ask), "Sell" = taker sold.
// `v` is size in base (BTC), `p` is price, `T` is the trade timestamp in ms.
type bybitPublicTrade struct {
	Topic string `json:"topic"`
	Data  []struct {
		Time  int64  `json:"T"`
		Side  string `json:"S"`
		Size  string `json:"v"`
		Price string `json:"p"`
	} `json:"data"`
}

// MaintainBybitPerpTrades feeds perp taker flow (for CVD, §6) from the linear
// BTCUSDT publicTrade stream into the FlowTracker.
func MaintainBybitPerpTrades(flow *aggregator.FlowTracker) {
	maintainBybitTrades("PERP-TRADE", bybitLinearWsURL, func(ts time.Time, signedUSD float64) {
		flow.AddPerpTrade(ts, signedUSD)
	})
}

// MaintainBybitSpotTrades feeds spot taker flow (for Spot-vs-Perp, §6) from the
// spot BTCUSDT publicTrade stream into the FlowTracker.
func MaintainBybitSpotTrades(flow *aggregator.FlowTracker) {
	maintainBybitTrades("SPOT-TRADE", bybitSpotWsURL, func(ts time.Time, signedUSD float64) {
		flow.AddSpotTrade(ts, signedUSD)
	})
}

// maintainBybitTrades runs the reconnect/ping loop for a publicTrade.BTCUSDT
// subscription and routes each fill's signed quote-volume to `record`.
func maintainBybitTrades(tag, wsURL string, record func(ts time.Time, signedUSD float64)) {
	for {
		log.Printf("[BYBIT] Connecting to %s stream: %s", tag, wsURL)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[BYBIT] %s dial error: %v. Reconnecting in 5s...", tag, err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op":   "subscribe",
			"args": []string{"publicTrade.BTCUSDT"},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("[BYBIT] %s subscribe error: %v. Reconnecting in 5s...", tag, err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("[BYBIT] %s stream connected. Subscribed to publicTrade.BTCUSDT.", tag)
		listenBybitTrades(conn, record)
		conn.Close()
		log.Printf("[BYBIT] %s stream disconnected. Reconnecting in 5s...", tag)
		time.Sleep(5 * time.Second)
	}
}

func listenBybitTrades(conn *websocket.Conn, record func(ts time.Time, signedUSD float64)) {
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
			var event bybitPublicTrade
			if err := json.Unmarshal(message, &event); err != nil || len(event.Data) == 0 {
				continue
			}
			for _, d := range event.Data {
				price, _ := strconv.ParseFloat(d.Price, 64)
				size, _ := strconv.ParseFloat(d.Size, 64)
				usd := size * price
				if d.Side == "Sell" || d.Side == "sell" {
					usd = -usd // taker-sell removes from net flow
				}
				record(time.UnixMilli(d.Time).UTC(), usd)
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
