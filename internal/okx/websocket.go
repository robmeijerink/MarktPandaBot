package okx

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

const (
	okxWsURL        = "wss://ws.okx.com:8443/ws/v5/public"
	okxPingInterval = 20 * time.Second
	okxBTCInst      = "BTC-USDT-SWAP"
	// BTC-USDT-SWAP is a linear contract worth 0.01 BTC each (ctVal=0.01),
	// so liquidation size in contracts is converted to BTC via this factor.
	okxBTCContractSize = 0.01
)

// liquidation-orders message. `data[].details[]` carries the actual fills.
type OKXLiquidation struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Data []struct {
		InstID  string `json:"instId"`
		Details []struct {
			BkPx string `json:"bkPx"` // bankruptcy price
			Sz   string `json:"sz"`   // size in contracts
			Side string `json:"side"` // buy = short liq, sell = long liq
		} `json:"details"`
	} `json:"data"`
}

// okxChannelMsg is the generic envelope for the context streams; the concrete
// payload is decoded per channel from Data[0].
type okxChannelMsg struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Data []json.RawMessage `json:"data"`
}

type okxFundingData struct {
	FundingRate string `json:"fundingRate"`
}

type okxOIData struct {
	OiUsd string `json:"oiUsd"`
}

type okxTickerData struct {
	Last    string `json:"last"`
	Open24h string `json:"open24h"`
}

// MaintainOKXLiquidations is the primary trigger feed: BTC liquidations on OKX.
// OKX replaces Binance because Binance USDⓈ-M Futures is geo-restricted from
// US regions (e.g. GCE us-central1), whereas OKX public data is reachable.
func MaintainOKXLiquidations(aggr *aggregator.Aggregator) {
	for {
		log.Printf("[OKX] Connecting to Liquidation stream: %s", okxWsURL)
		conn, _, err := websocket.DefaultDialer.Dial(okxWsURL, nil)
		if err != nil {
			log.Printf("[OKX] Liquidation dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op": "subscribe",
			"args": []map[string]string{
				{"channel": "liquidation-orders", "instType": "SWAP"},
			},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("[OKX] Liquidation subscribe error: %v. Reconnecting in 5s...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[OKX] Liquidation stream connected. Subscribed to liquidation-orders (SWAP), filtering BTC-USDT-SWAP.")
		listenOKXLiquidations(conn, aggr)
		conn.Close()
		log.Println("[OKX] Liquidation stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func listenOKXLiquidations(conn *websocket.Conn, aggr *aggregator.Aggregator) {
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
			var event OKXLiquidation
			if err := json.Unmarshal(message, &event); err != nil || len(event.Data) == 0 {
				continue
			}
			for _, d := range event.Data {
				if d.InstID != okxBTCInst {
					continue
				}
				for _, det := range d.Details {
					price, _ := strconv.ParseFloat(det.BkPx, 64)
					contracts, _ := strconv.ParseFloat(det.Sz, 64)
					btcQty := contracts * okxBTCContractSize

					// Normalise side to upper-case so the engine's SELL/BUY
					// (long/short) classification matches the other feeds.
					side := strings.ToUpper(det.Side)

					log.Printf("[OKX] Liquidation received: %s %s qty=%.4f ₿ price=%.2f (~%.0f $)",
						d.InstID, side, btcQty, price, btcQty*price)

					aggr.AddEvent(aggregator.LiquidationEvent{
						Exchange: "okx",
						Symbol:   d.InstID,
						Price:    price,
						Qty:      btcQty,
						Side:     side,
					})
				}
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			// OKX closes idle connections after 30s; keep it alive.
			if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
				return
			}
		}
	}
}

// MaintainOKXContext feeds background context for the alert message into
// MarketState: funding rate, open interest (USD), and last/24h-open price.
func MaintainOKXContext(state *aggregator.MarketState) {
	for {
		log.Printf("[OKX] Connecting to Context stream: %s", okxWsURL)
		conn, _, err := websocket.DefaultDialer.Dial(okxWsURL, nil)
		if err != nil {
			log.Printf("[OKX] Context dial error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		subMsg := map[string]interface{}{
			"op": "subscribe",
			"args": []map[string]string{
				{"channel": "funding-rate", "instId": okxBTCInst},
				{"channel": "open-interest", "instId": okxBTCInst},
				{"channel": "tickers", "instId": okxBTCInst},
			},
		}
		if err := conn.WriteJSON(subMsg); err != nil {
			log.Printf("[OKX] Context subscribe error: %v. Reconnecting in 5s...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("[OKX] Context stream connected. Subscribed to funding-rate, open-interest, tickers (BTC-USDT-SWAP).")
		listenOKXContext(conn, state)
		conn.Close()
		log.Println("[OKX] Context stream disconnected. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func listenOKXContext(conn *websocket.Conn, state *aggregator.MarketState) {
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
			var msg okxChannelMsg
			if err := json.Unmarshal(message, &msg); err != nil || len(msg.Data) == 0 {
				continue
			}
			switch msg.Arg.Channel {
			case "funding-rate":
				var d okxFundingData
				if json.Unmarshal(msg.Data[0], &d) == nil && d.FundingRate != "" {
					rate, _ := strconv.ParseFloat(d.FundingRate, 64)
					state.Mu.Lock()
					state.OKXFunding = rate
					state.Mu.Unlock()
				}
			case "open-interest":
				var d okxOIData
				if json.Unmarshal(msg.Data[0], &d) == nil && d.OiUsd != "" {
					oi, _ := strconv.ParseFloat(d.OiUsd, 64)
					state.Mu.Lock()
					state.OKXOI = oi
					state.Mu.Unlock()
				}
			case "tickers":
				var d okxTickerData
				if json.Unmarshal(msg.Data[0], &d) == nil {
					state.Mu.Lock()
					if d.Last != "" {
						p, _ := strconv.ParseFloat(d.Last, 64)
						state.OKXLastPrice = p
					}
					if d.Open24h != "" {
						o, _ := strconv.ParseFloat(d.Open24h, 64)
						state.OKXOpen24h = o
					}
					state.Mu.Unlock()
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
