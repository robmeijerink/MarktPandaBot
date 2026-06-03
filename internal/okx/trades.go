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

// okxFlow is one decoded fill: trade time, signed quote/USD notional, and whether
// it is a perp (true) or spot (false) trade.
type okxFlow struct {
	ts   time.Time
	usd  float64
	perp bool
}

// decodeOKXTrades parses a "trades" channel message into signed flows. Perp size
// is in contracts (×okxBTCContractSize BTC); spot size is already in base (BTC). A
// taker-sell ("sell") is negative. Instruments other than the BTC perp/spot are
// ignored.
func decodeOKXTrades(message []byte) []okxFlow {
	var msg okxTradeMsg
	if err := json.Unmarshal(message, &msg); err != nil || len(msg.Data) == 0 {
		return nil
	}
	out := make([]okxFlow, 0, len(msg.Data))
	for _, d := range msg.Data {
		price, _ := strconv.ParseFloat(d.Px, 64)
		sz, _ := strconv.ParseFloat(d.Sz, 64)
		tsMs, _ := strconv.ParseInt(d.Ts, 10, 64)
		ts := time.UnixMilli(tsMs).UTC()

		var usd float64
		var perp bool
		switch d.InstID {
		case okxBTCInst: // perp: size is in contracts
			usd = sz * okxBTCContractSize * price
			perp = true
		case okxBTCSpotInst: // spot: size is in base (BTC)
			usd = sz * price
		default:
			continue
		}
		if d.Side == "sell" {
			usd = -usd
		}
		out = append(out, okxFlow{ts: ts, usd: usd, perp: perp})
	}
	return out
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
			for _, f := range decodeOKXTrades(message) {
				if f.perp {
					flow.AddPerpTrade(f.ts, f.usd)
				} else {
					flow.AddSpotTrade(f.ts, f.usd)
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
