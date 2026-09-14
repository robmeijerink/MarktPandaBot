package sweep

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const (
	bybitWSURL       = "wss://stream.bybit.com/v5/public/linear"
	okxWSURL         = "wss://ws.okx.com:8443/ws/v5/public"
	okxInstID        = "BTC-USDT-SWAP"
	bybitPingEvery   = 18 * time.Second
	okxPingEvery     = 20 * time.Second
	wsReconnectDelay = 5 * time.Second
)

// bybitEnvelope covers both data pushes (topic + data) and the subscribe ack.
type bybitEnvelope struct {
	Topic   string          `json:"topic"`
	Op      string          `json:"op"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

// runBybitFeed keeps one Bybit connection subscribed to the candle and liquidation
// topics, forwarding closed candles to bars and liquidations to the store. It marks
// Bybit's liquidation coverage up only once the subscription is acknowledged, and
// down the moment the connection drops. It reconnects forever.
func runBybitFeed(cfg Config, bars chan<- Bar, liq *liqStore) {
	klineTopic := "kline." + strconv.Itoa(int(cfg.step()/time.Minute)) + "." + cfg.Symbol
	liqTopic := "allLiquidation." + cfg.Symbol
	for {
		conn, _, err := websocket.DefaultDialer.Dial(bybitWSURL, nil)
		if err != nil {
			log.Printf("[SWEEP] Bybit WS dial error: %v. Reconnecting in %s...", err, wsReconnectDelay)
			time.Sleep(wsReconnectDelay)
			continue
		}
		if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": []string{klineTopic, liqTopic}}); err != nil {
			log.Printf("[SWEEP] Bybit WS subscribe error: %v. Reconnecting in %s...", err, wsReconnectDelay)
			conn.Close()
			time.Sleep(wsReconnectDelay)
			continue
		}
		log.Printf("[SWEEP] Bybit WS connected (%s, %s).", klineTopic, liqTopic)
		keepAlive(conn, bybitPingEvery, func() error { return conn.WriteJSON(map[string]string{"op": "ping"}) }, func(raw []byte) {
			var env bybitEnvelope
			if json.Unmarshal(raw, &env) != nil {
				return
			}
			switch {
			case env.Op == "subscribe":
				liq.setUp(venueBybit, env.Success, time.Now())
				if !env.Success {
					log.Printf("[SWEEP] Bybit WS subscription rejected: %s", raw)
				}
			case env.Topic == klineTopic:
				for _, b := range parseBybitKlines(env.Data) {
					bars <- b
				}
			case env.Topic == liqTopic:
				liq.add(parseBybitLiquidations(env.Data)...)
			}
		})
		liq.setUp(venueBybit, false, time.Now())
		conn.Close()
		log.Printf("[SWEEP] Bybit WS disconnected. Reconnecting in %s...", wsReconnectDelay)
		time.Sleep(wsReconnectDelay)
	}
}

// runOKXFeed keeps an OKX connection subscribed to SWAP liquidation orders (filtered
// to BTC-USDT-SWAP). It reconnects forever.
func runOKXFeed(liq *liqStore) {
	for {
		conn, _, err := websocket.DefaultDialer.Dial(okxWSURL, nil)
		if err != nil {
			log.Printf("[SWEEP] OKX WS dial error: %v. Reconnecting in %s...", err, wsReconnectDelay)
			time.Sleep(wsReconnectDelay)
			continue
		}
		sub := map[string]any{"op": "subscribe", "args": []map[string]string{{"channel": "liquidation-orders", "instType": "SWAP"}}}
		if err := conn.WriteJSON(sub); err != nil {
			log.Printf("[SWEEP] OKX WS subscribe error: %v. Reconnecting in %s...", err, wsReconnectDelay)
			conn.Close()
			time.Sleep(wsReconnectDelay)
			continue
		}
		log.Println("[SWEEP] OKX WS connected (liquidation-orders).")
		keepAlive(conn, okxPingEvery, func() error { return conn.WriteMessage(websocket.TextMessage, []byte("ping")) }, func(raw []byte) {
			var ev struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(raw, &ev) == nil && ev.Event != "" {
				// Only the subscribe ack and errors change coverage; OKX also sends
				// informational events (e.g. channel-conn-count) that must not.
				switch ev.Event {
				case "subscribe":
					liq.setUp(venueOKX, true, time.Now())
				case "error":
					liq.setUp(venueOKX, false, time.Now())
					log.Printf("[SWEEP] OKX WS error event: %s", raw)
				}
				return
			}
			liq.add(parseOKXLiquidations(raw, okxInstID)...)
		})
		liq.setUp(venueOKX, false, time.Now())
		conn.Close()
		log.Printf("[SWEEP] OKX WS disconnected. Reconnecting in %s...", wsReconnectDelay)
		time.Sleep(wsReconnectDelay)
	}
}

// keepAlive reads messages into handle and pings every `every` until the connection
// fails.
func keepAlive(conn *websocket.Conn, every time.Duration, ping func() error, handle func([]byte)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			handle(raw)
		}
	}()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := ping(); err != nil {
				conn.Close()
				<-done
				return
			}
		}
	}
}

// parseBybitKlines decodes a kline topic's data array, keeping only closed (confirm) candles.
func parseBybitKlines(data json.RawMessage) []Bar {
	var rows []struct {
		Start   int64  `json:"start"`
		Open    string `json:"open"`
		High    string `json:"high"`
		Low     string `json:"low"`
		Close   string `json:"close"`
		Volume  string `json:"volume"`
		Confirm bool   `json:"confirm"`
	}
	if json.Unmarshal(data, &rows) != nil {
		return nil
	}
	var out []Bar
	for _, r := range rows {
		if !r.Confirm || r.Start == 0 {
			continue
		}
		b, ok := parseRESTBar([]string{strconv.FormatInt(r.Start, 10), r.Open, r.High, r.Low, r.Close, r.Volume})
		if ok {
			out = append(out, b)
		}
	}
	return out
}
