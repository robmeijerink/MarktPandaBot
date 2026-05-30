package aggregator

import (
	"fmt"
	"sync"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/telegram"
)

const (
	EvaluationInterval      = 5 * time.Minute
	BinanceTriggerVolumeBTC = 5.0
	BybitConfirmVolumeUSDT  = 100000.0
)

// MarketState holds the real-time background data updated by secondary WebSockets
type MarketState struct {
	Mu             sync.RWMutex
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

func RunConfluenceEngine(aggregator *Aggregator, state *MarketState, token, chatID string) {
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
			state.Mu.RLock()
			currentBinanceFunding := state.BinanceFunding
			currentBybitFunding := state.BybitFunding
			currentBybitOI := state.BybitOI
			state.Mu.RUnlock()

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

			telegram.DispatchTelegramAlert(token, chatID, msg)
		}
	}
}
