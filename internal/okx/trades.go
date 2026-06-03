package okx

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

// okxBTCSpotInst is the spot instrument; the perp is okxBTCInst (…-SWAP). Both
// trade streams share the one public WS endpoint and the "trades" channel, routed
// by instId.
const okxBTCSpotInst = "BTC-USDT"

// okxTradeMsg is a public "trades" channel payload. `side` is the TAKER aggressor
// side ("buy"/"sell"). For SWAP, `sz` is in contracts (×okxBTCContractSize BTC);
// for spot it is already in base (BTC). `ts` is the trade time in ms.
type okxTradeMsg struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Data []struct {
		InstID string `json:"instId"`
		Px     string `json:"px"`
		Sz     string `json:"sz"`
		Side   string `json:"side"`
		Ts     string `json:"ts"`
	} `json:"data"`
}

// MaintainOKXTrades subscribes to perp and spot BTC trades on the single public
// endpoint and feeds CVD (perp) and Spot-vs-Perp (spot) flow into the tracker.
func MaintainOKXTrades(flow *aggregator.FlowTracker) {
	for {
		log.Printf("[OKX] Connecting to Trades stream: %s", okxWsURL)
		conn, _, err := websocket.DefaultDialer.Dial(okxWsURL, nil)
		if err != nil {
			log.Printf("[OKX] Trades dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op": "subscribe",
			"args": []map[string]string{
				{"channel": "trades", "instId": okxBTCInst},
				{"channel": "trades", "instId": okxBTCSpotInst},
			},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("[OKX] Trades subscribe error: %v. Reconnecting in 5s...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[OKX] Trades stream connected. Subscribed to trades (BTC-USDT-SWAP, BTC-USDT).")
		listenOKXTrades(conn, flow)
		conn.Close()
		log.Println("[OKX] Trades stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func listenOKXTrades(conn *websocket.Conn, flow *aggregator.FlowTracker) {
	pingTicker := time.NewTicker(okxPingInterval)
	defer pingTicker.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg okxTradeMsg
			if err := json.Unmarshal(message, &msg); err != nil || len(msg.Data) == 0 {
				continue
			}
			for _, d := range msg.Data {
				price, _ := strconv.ParseFloat(d.Px, 64)
				sz, _ := strconv.ParseFloat(d.Sz, 64)
				tsMs, _ := strconv.ParseInt(d.Ts, 10, 64)
				ts := time.UnixMilli(tsMs).UTC()

				switch d.InstID {
				case okxBTCInst: // perp: size is in contracts
					usd := sz * okxBTCContractSize * price
					if d.Side == "sell" {
						usd = -usd
					}
					flow.AddPerpTrade(ts, usd)
				case okxBTCSpotInst: // spot: size is in base (BTC)
					usd := sz * price
					if d.Side == "sell" {
						usd = -usd
					}
					flow.AddSpotTrade(ts, usd)
				}
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
				return
			}
		}
	}
}
