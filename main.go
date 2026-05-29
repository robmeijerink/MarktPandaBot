package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	EvaluationInterval      = 5 * time.Minute
	BinanceTriggerVolumeBTC = 5.0
	BybitConfirmVolumeUSDT  = 100000.0
	HealthCheckPort         = ":8080"
)

// MarketState holds the real-time background data updated by secondary WebSockets
type MarketState struct {
	mu             sync.RWMutex
	BinanceFunding float64
	BybitFunding   float64
	BybitOI        float64
}

type LiquidationEvent struct {
	Exchange string
	Symbol   string
	Price    float64
	Qty      float64
	Side     string
}

type Aggregator struct {
	mu     sync.Mutex
	events []LiquidationEvent
}

func (a *Aggregator) AddEvent(event LiquidationEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

func (a *Aggregator) ExtractAndClear() []LiquidationEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	data := a.events
	a.events = nil
	return data
}

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

func main() {
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if telegramToken == "" || chatID == "" {
		log.Fatal("Environment variables TELEGRAM_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	aggregator := &Aggregator{}
	state := &MarketState{}

	// Primary Streams (Liquidations)
	go maintainBinanceForceOrders(aggregator)
	go maintainBybitLiquidations(aggregator)

	// Secondary Streams (Stateful Context: Funding & OI)
	go maintainBinanceMarkPrice(state)
	go maintainBybitTickers(state)

	// Decision Engine
	go runConfluenceEngine(aggregator, state, telegramToken, chatID)

	// Health Check for Docker/Google Cloud Engine
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Printf("Starting application. Health check listening on %s", HealthCheckPort)
	if err := http.ListenAndServe(HealthCheckPort, nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}

// --- BINANCE STREAMS ---

func maintainBinanceForceOrders(aggregator *Aggregator) {
	wsURL := "wss://fstream.binance.com/ws/btcusdt@forceOrder"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("Binance ForceOrder Dial Error: %v. Reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		listenBinanceForceOrders(conn, aggregator)
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

func listenBinanceForceOrders(conn *websocket.Conn, aggregator *Aggregator) {
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

		aggregator.AddEvent(LiquidationEvent{
			Exchange: "binance",
			Symbol:   event.Order.Symbol,
			Price:    price,
			Qty:      qty,
			Side:     event.Order.Side,
		})
	}
}

func maintainBinanceMarkPrice(state *MarketState) {
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
				state.mu.Lock()
				state.BinanceFunding = rate
				state.mu.Unlock()
			}
		}
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

// --- BYBIT STREAMS ---

func maintainBybitLiquidations(aggregator *Aggregator) {
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
		listenBybitLiquidations(conn, aggregator)
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

func listenBybitLiquidations(conn *websocket.Conn, aggregator *Aggregator) {
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

		aggregator.AddEvent(LiquidationEvent{
			Exchange: "bybit",
			Symbol:   event.Data.Symbol,
			Price:    price,
			Qty:      volumeUSDT,
			Side:     event.Data.Side,
		})
	}
}

func maintainBybitTickers(state *MarketState) {
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
					state.mu.Lock()
					if event.Data.FundingRate != "" {
						rate, _ := strconv.ParseFloat(event.Data.FundingRate, 64)
						state.BybitFunding = rate
					}
					if event.Data.OpenInterest != "" {
						oi, _ := strconv.ParseFloat(event.Data.OpenInterest, 64)
						state.BybitOI = oi
					}
					state.mu.Unlock()
				}
			}
		}
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

// --- ENGINE & TELEGRAM ---

func runConfluenceEngine(aggregator *Aggregator, state *MarketState, token, chatID string) {
	ticker := time.NewTicker(EvaluationInterval)
	defer ticker.Stop()

	for range ticker.C {
		data := aggregator.ExtractAndClear()
		if len(data) == 0 {
			continue
		}

		var binanceVol, bybitVolUSDT float64
		var binanceMin, binanceMax float64
		var bybitMin, bybitMax float64
		binanceCount, bybitCount := 0, 0

		for _, item := range data {
			if item.Exchange == "binance" {
				binanceVol += item.Qty
				if binanceCount == 0 || item.Price < binanceMin {
					binanceMin = item.Price
				}
				if binanceCount == 0 || item.Price > binanceMax {
					binanceMax = item.Price
				}
				binanceCount++
			} else if item.Exchange == "bybit" {
				bybitVolUSDT += item.Qty
				if bybitCount == 0 || item.Price < bybitMin {
					bybitMin = item.Price
				}
				if bybitCount == 0 || item.Price > bybitMax {
					bybitMax = item.Price
				}
				bybitCount++
			}
		}

		isGlobalTrigger := binanceVol >= BinanceTriggerVolumeBTC
		isLocalConfirmed := bybitVolUSDT >= BybitConfirmVolumeUSDT

		if isGlobalTrigger && isLocalConfirmed {
			// Safely read background context
			state.mu.RLock()
			currentBinanceFunding := state.BinanceFunding
			currentBybitFunding := state.BybitFunding
			currentBybitOI := state.BybitOI
			state.mu.RUnlock()

			msg := fmt.Sprintf(
				"🚨 *CONFLUENCE*\n\n"+
					"🌐 *BINANCE*\n"+
					"Vol: %.2f ₿\n"+
					"Ord: %d\n"+
					"Rng: %.0f - %.0f\n"+
					"Fund: %.4f%%\n\n"+
					"📍 *BYBIT*\n"+
					"Vol: %.0f $\n"+
					"Ord: %d\n"+
					"Rng: %.0f - %.0f\n"+
					"Fund: %.4f%%\n"+
					"OI: %.0f",
				binanceVol, binanceCount, binanceMin, binanceMax, (currentBinanceFunding * 100),
				bybitVolUSDT, bybitCount, bybitMin, bybitMax, (currentBybitFunding * 100), currentBybitOI,
			)

			dispatchTelegramAlert(token, chatID, msg)
		}
	}
}

func dispatchTelegramAlert(token, chatID, msg string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       msg,
		"parse_mode": "Markdown",
	}
	jsonPayload, _ := json.Marshal(payload)
	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("Failed to send HTTP request to Telegram: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("Telegram API returned non-200 status code: %d", resp.StatusCode)
	}
}
