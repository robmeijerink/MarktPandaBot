package aggregator

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/telegram"
)

const (
	EvaluationInterval      = 5 * time.Minute
	BinanceTriggerVolumeBTC = 3.0
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

	log.Printf("[ENGINE] Confluence engine started. Evaluating every %s "+
		"(triggers: Binance >= %.2f ₿ AND Bybit >= %.0f $)",
		EvaluationInterval, BinanceTriggerVolumeBTC, BybitConfirmVolumeUSDT)

	for range ticker.C {
		data := aggregator.ExtractAndClear()
		if len(data) == 0 {
			log.Println("[ENGINE] Evaluation cycle: 0 liquidation events. Idle.")
			continue
		}
		log.Printf("[ENGINE] Evaluation cycle: %d liquidation events. Aggregating...", len(data))

		var binanceVol, binanceVolUSDT float64
		var binanceLongVol, binanceLongVolUSDT float64
		var binanceShortVol, binanceShortVolUSDT float64

		var bybitVolUSDT, bybitLongVolUSDT, bybitShortVolUSDT float64

		var binanceMin, binanceMax float64
		var bybitMin, bybitMax float64
		binanceCount, bybitCount := 0, 0

		for _, item := range data {
			isLongLiq := item.Side == "SELL" || item.Side == "Sell"
			isShortLiq := item.Side == "BUY" || item.Side == "Buy"

			if item.Exchange == "binance" {
				tradeValueUSDT := item.Qty * item.Price
				binanceVol += item.Qty
				binanceVolUSDT += tradeValueUSDT

				if isLongLiq {
					binanceLongVol += item.Qty
					binanceLongVolUSDT += tradeValueUSDT
				} else if isShortLiq {
					binanceShortVol += item.Qty
					binanceShortVolUSDT += tradeValueUSDT
				}

				if binanceCount == 0 || item.Price < binanceMin {
					binanceMin = item.Price
				}
				if binanceCount == 0 || item.Price > binanceMax {
					binanceMax = item.Price
				}
				binanceCount++
			} else if item.Exchange == "bybit" {
				bybitVolUSDT += item.Qty
				if isLongLiq {
					bybitLongVolUSDT += item.Qty
				} else if isShortLiq {
					bybitShortVolUSDT += item.Qty
				}

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

		log.Printf("[ENGINE] Breakdown -> Binance: %d ord, %.2f ₿ (~%.0f $) | "+
			"Bybit: %d ord, %.0f $",
			binanceCount, binanceVol, binanceVolUSDT, bybitCount, bybitVolUSDT)
		log.Printf("[ENGINE] Trigger check -> globalTrigger(Binance>=%.2f₿)=%t | "+
			"localConfirmed(Bybit>=%.0f$)=%t",
			BinanceTriggerVolumeBTC, isGlobalTrigger, BybitConfirmVolumeUSDT, isLocalConfirmed)

		// Diagnostic: alerts require BOTH legs. Warn loudly when one leg has
		// volume but the other is silent, so a broken feed is visible.
		if binanceCount > 0 && bybitCount == 0 {
			log.Println("[ENGINE] WARNING: Binance has liquidations but Bybit feed is EMPTY " +
				"this cycle. Bybit confirmation impossible — check the Bybit stream.")
		} else if bybitCount > 0 && binanceCount == 0 {
			log.Println("[ENGINE] WARNING: Bybit has liquidations but Binance feed is EMPTY " +
				"this cycle. Check the Binance stream.")
		}

		if !isGlobalTrigger || !isLocalConfirmed {
			log.Println("[ENGINE] No alert: confluence thresholds not met (both legs required).")
		}

		if isGlobalTrigger && isLocalConfirmed {
			log.Println("[ENGINE] ALERT TRIGGERED: both thresholds met. Building message...")
			state.Mu.RLock()
			currentBinanceFunding := state.BinanceFunding
			currentBybitFunding := state.BybitFunding
			currentBybitOI := state.BybitOI
			state.Mu.RUnlock()

			totalImpactUSDT := binanceVolUSDT + bybitVolUSDT

			msg := fmt.Sprintf(
				"🚨 *LIQUIDATION ALERT*\n"+
					"_⚠️ Combined (Binance & Bybit): ~%.0f $ liquidated in the past 5 minutes._\n\n"+
					"🌐 *BINANCE* (Total: %.2f ₿ / ~%.0f $)\n"+
					"🔴 Longs: %.2f ₿ (~%.0f $)\n"+
					"🟢 Shorts: %.2f ₿ (~%.0f $)\n"+
					"Ord: %d\n"+
					"Rng: %.0f - %.0f\n"+
					"Fund: %.4f%%\n\n"+
					"📍 *BYBIT* (Total: %.0f $)\n"+
					"🔴 Longs: %.0f $\n"+
					"🟢 Shorts: %.0f $\n"+
					"Ord: %d\n"+
					"Rng: %.0f - %.0f\n"+
					"Fund: %.4f%%\n"+
					"OI: %.0f",
				totalImpactUSDT,                                                                                                                                                           // Intro
				binanceVol, binanceVolUSDT, binanceLongVol, binanceLongVolUSDT, binanceShortVol, binanceShortVolUSDT, binanceCount, binanceMin, binanceMax, (currentBinanceFunding * 100), // Binance
				bybitVolUSDT, bybitLongVolUSDT, bybitShortVolUSDT, bybitCount, bybitMin, bybitMax, (currentBybitFunding * 100), currentBybitOI, // Bybit
			)

			log.Printf("[ENGINE] Dispatching Telegram alert (combined impact ~%.0f $)...", totalImpactUSDT)
			telegram.DispatchTelegramAlert(token, chatID, msg)
		}
	}
}
