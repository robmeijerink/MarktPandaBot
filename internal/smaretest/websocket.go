package smaretest

import (
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const (
	bybitWSURL       = "wss://stream.bybit.com/v5/public/linear"
	bybitPingEvery   = 18 * time.Second
	wsReconnectDelay = 5 * time.Second
)

// wsKlineEntry is one candle in a Bybit v5 kline payload.
type wsKlineEntry struct {
	Start   int64  `json:"start"` // candle open time, ms
	Open    string `json:"open"`
	High    string `json:"high"`
	Low     string `json:"low"`
	Close   string `json:"close"`
	Confirm bool   `json:"confirm"`
}

// wsKlineMsg is the Bybit v5 kline.{interval}.{symbol} payload. Only `confirm`
// (finalized) bars are forwarded.
type wsKlineMsg struct {
	Topic string         `json:"topic"`
	Data  []wsKlineEntry `json:"data"`
}

// runKlineWS maintains the primary 3m kline WebSocket subscription and pushes one
// Bar per finalized (confirm) candle onto barCh. It reconnects forever; it never
// touches the existing liquidation/ticker WS handlers. Bars are de-duplicated by
// the consumer, so a brief overlap with the REST fallback is harmless.
func runKlineWS(cfg Config, barCh chan<- Bar) {
	topic := "kline." + bybitInterval(cfg.Timeframe) + "." + cfg.Symbol
	for {
		log.Printf("[SMARETEST] Connecting to 3m kline WS: %s (%s)", bybitWSURL, topic)
		conn, _, err := websocket.DefaultDialer.Dial(bybitWSURL, nil)
		if err != nil {
			log.Printf("[SMARETEST] Kline WS dial error: %v. Reconnecting in %s...", err, wsReconnectDelay)
			time.Sleep(wsReconnectDelay)
			continue
		}
		sub := map[string]interface{}{"op": "subscribe", "args": []string{topic}}
		if err := conn.WriteJSON(sub); err != nil {
			log.Printf("[SMARETEST] Kline WS subscribe error: %v. Reconnecting in %s...", err, wsReconnectDelay)
			conn.Close()
			time.Sleep(wsReconnectDelay)
			continue
		}
		log.Printf("[SMARETEST] Kline WS connected. Subscribed to %s.", topic)
		listenKlineWS(conn, barCh)
		conn.Close()
		log.Printf("[SMARETEST] Kline WS disconnected. Reconnecting in %s...", wsReconnectDelay)
		time.Sleep(wsReconnectDelay)
	}
}

func listenKlineWS(conn *websocket.Conn, barCh chan<- Bar) {
	pingTicker := time.NewTicker(bybitPingEvery)
	defer pingTicker.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg wsKlineMsg
			if err := json.Unmarshal(message, &msg); err != nil || len(msg.Data) == 0 {
				continue
			}
			for _, d := range msg.Data {
				if !d.Confirm {
					continue // only finalized bars enter the state machine
				}
				b, ok := parseWSBar(d)
				if !ok {
					continue
				}
				barCh <- b
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

func parseWSBar(d wsKlineEntry) (Bar, bool) {
	if d.Start == 0 {
		return Bar{}, false
	}
	open, _ := strconv.ParseFloat(d.Open, 64)
	high, _ := strconv.ParseFloat(d.High, 64)
	low, _ := strconv.ParseFloat(d.Low, 64)
	cl, err := strconv.ParseFloat(d.Close, 64)
	if err != nil {
		return Bar{}, false
	}
	return Bar{
		BucketStart: time.UnixMilli(d.Start).UTC(),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       cl,
	}, true
}
