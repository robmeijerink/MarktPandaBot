package aggregator

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/telegram"
)

const (
	EvaluationInterval = 5 * time.Minute
	// Both legs use the same USDT threshold: an alert needs OKX liquidations
	// >= this AND Bybit liquidations >= this within one evaluation window.
	TriggerVolumeUSDT = 200000.0
)

// MarketState holds the real-time background data updated by secondary
// WebSockets. OI values are in USD on both legs so they are comparable.
type MarketState struct {
	Mu           sync.RWMutex
	OKXFunding   float64
	OKXOI        float64 // USD (oiUsd)
	OKXLastPrice float64
	OKXOpen24h   float64
	BybitFunding float64
	BybitOI      float64 // USD (openInterestValue)
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

// humanUSD formats a USD amount compactly: $200k, $12.3M, $2.56B.
func humanUSD(v float64) string {
	a := math.Abs(v)
	sign := ""
	if v < 0 {
		sign = "-"
	}
	switch {
	case a >= 1e9:
		return fmt.Sprintf("%s$%.2fB", sign, a/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%s$%.1fM", sign, a/1e6)
	case a >= 1e3:
		return fmt.Sprintf("%s$%.0fk", sign, a/1e3)
	default:
		return fmt.Sprintf("%s$%.0f", sign, a)
	}
}

// signedUSD formats a delta with an explicit + for non-negative values.
func signedUSD(v float64) string {
	if v >= 0 {
		return "+" + humanUSD(v)
	}
	return humanUSD(v)
}

func sideLabel(isLong, isShort bool) string {
	if isLong {
		return "long"
	}
	if isShort {
		return "short"
	}
	return "?"
}

func RunConfluenceEngine(aggregator *Aggregator, state *MarketState, token, chatID string) {
	ticker := time.NewTicker(EvaluationInterval)
	defer ticker.Stop()

	log.Printf("[ENGINE] Confluence engine started. Evaluating every %s "+
		"(trigger: BOTH OKX and Bybit >= %s)",
		EvaluationInterval, humanUSD(TriggerVolumeUSDT))

	// OI baselines for the 5-minute delta, carried across cycles.
	var prevOKXOI, prevBybitOI float64
	var oiBaseline bool

	for range ticker.C {
		// Snapshot background context every cycle (incl. idle ones) so the OI
		// delta always reflects the full window when an alert eventually fires.
		state.Mu.RLock()
		curOKXFunding := state.OKXFunding
		curOKXOI := state.OKXOI
		okxLast := state.OKXLastPrice
		okxOpen24h := state.OKXOpen24h
		curBybitFunding := state.BybitFunding
		curBybitOI := state.BybitOI
		state.Mu.RUnlock()

		var okxOIDelta, bybitOIDelta float64
		if oiBaseline {
			if prevOKXOI > 0 && curOKXOI > 0 {
				okxOIDelta = curOKXOI - prevOKXOI
			}
			if prevBybitOI > 0 && curBybitOI > 0 {
				bybitOIDelta = curBybitOI - prevBybitOI
			}
		}
		prevOKXOI = curOKXOI
		prevBybitOI = curBybitOI
		oiBaseline = true

		data := aggregator.ExtractAndClear()
		if len(data) == 0 {
			log.Println("[ENGINE] Evaluation cycle: 0 liquidation events. Idle.")
			continue
		}
		log.Printf("[ENGINE] Evaluation cycle: %d liquidation events. Aggregating...", len(data))

		var okxVol, okxVolUSDT float64
		var okxLongVolUSDT, okxShortVolUSDT float64
		var okxMin, okxMax float64
		var okxBiggestUSDT float64
		var okxBiggestSide string
		okxCount := 0

		var bybitVolUSDT, bybitLongVolUSDT, bybitShortVolUSDT float64
		var bybitMin, bybitMax float64
		var bybitBiggestUSDT float64
		var bybitBiggestSide string
		bybitCount := 0

		for _, item := range data {
			isLongLiq := item.Side == "SELL" || item.Side == "Sell"
			isShortLiq := item.Side == "BUY" || item.Side == "Buy"

			if item.Exchange == "okx" {
				tradeValueUSDT := item.Qty * item.Price
				okxVol += item.Qty
				okxVolUSDT += tradeValueUSDT

				if isLongLiq {
					okxLongVolUSDT += tradeValueUSDT
				} else if isShortLiq {
					okxShortVolUSDT += tradeValueUSDT
				}
				if tradeValueUSDT > okxBiggestUSDT {
					okxBiggestUSDT = tradeValueUSDT
					okxBiggestSide = sideLabel(isLongLiq, isShortLiq)
				}
				if okxCount == 0 || item.Price < okxMin {
					okxMin = item.Price
				}
				if okxCount == 0 || item.Price > okxMax {
					okxMax = item.Price
				}
				okxCount++
			} else if item.Exchange == "bybit" {
				bybitVolUSDT += item.Qty
				if isLongLiq {
					bybitLongVolUSDT += item.Qty
				} else if isShortLiq {
					bybitShortVolUSDT += item.Qty
				}
				if item.Qty > bybitBiggestUSDT {
					bybitBiggestUSDT = item.Qty
					bybitBiggestSide = sideLabel(isLongLiq, isShortLiq)
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

		isGlobalTrigger := okxVolUSDT >= TriggerVolumeUSDT
		isLocalConfirmed := bybitVolUSDT >= TriggerVolumeUSDT

		log.Printf("[ENGINE] Breakdown -> OKX: %d ord, %s | Bybit: %d ord, %s",
			okxCount, humanUSD(okxVolUSDT), bybitCount, humanUSD(bybitVolUSDT))
		log.Printf("[ENGINE] Trigger check -> OKX>=%s=%t | Bybit>=%s=%t",
			humanUSD(TriggerVolumeUSDT), isGlobalTrigger, humanUSD(TriggerVolumeUSDT), isLocalConfirmed)

		// Diagnostic: alerts require BOTH legs. Warn loudly when one leg has
		// volume but the other is silent, so a broken feed is visible.
		if okxCount > 0 && bybitCount == 0 {
			log.Println("[ENGINE] WARNING: OKX has liquidations but Bybit feed is EMPTY " +
				"this cycle. Bybit confirmation impossible — check the Bybit stream.")
		} else if bybitCount > 0 && okxCount == 0 {
			log.Println("[ENGINE] WARNING: Bybit has liquidations but OKX feed is EMPTY " +
				"this cycle. Check the OKX stream.")
		}

		if !isGlobalTrigger || !isLocalConfirmed {
			log.Println("[ENGINE] No alert: confluence thresholds not met (both legs required).")
			continue
		}

		log.Println("[ENGINE] ALERT TRIGGERED: both thresholds met. Building message...")

		totalImpactUSDT := okxVolUSDT + bybitVolUSDT
		okxPct := 0.0
		if okxOpen24h > 0 {
			okxPct = (okxLast - okxOpen24h) / okxOpen24h * 100
		}

		msg := fmt.Sprintf(
			"🚨 *LIQUIDATION ALERT*\n"+
				"_⚠️ Combined (OKX & Bybit): ~%s liquidated in the past 5 minutes._\n"+
				"📊 BTC $%.0f (%+.1f%% 24h)\n\n"+
				"🌐 *OKX* (Total: ~%s / %.2f ₿)\n"+
				"🔴 Longs: ~%s   🟢 Shorts: ~%s\n"+
				"Ord: %d   Biggest: ~%s %s\n"+
				"Rng: %.0f - %.0f\n"+
				"Fund: %.4f%%   OI: %s (Δ %s)\n\n"+
				"📍 *BYBIT* (Total: ~%s)\n"+
				"🔴 Longs: ~%s   🟢 Shorts: ~%s\n"+
				"Ord: %d   Biggest: ~%s %s\n"+
				"Rng: %.0f - %.0f\n"+
				"Fund: %.4f%%   OI: %s (Δ %s)",
			humanUSD(totalImpactUSDT),
			okxLast, okxPct,
			humanUSD(okxVolUSDT), okxVol,
			humanUSD(okxLongVolUSDT), humanUSD(okxShortVolUSDT),
			okxCount, humanUSD(okxBiggestUSDT), okxBiggestSide,
			okxMin, okxMax,
			curOKXFunding*100, humanUSD(curOKXOI), signedUSD(okxOIDelta),
			humanUSD(bybitVolUSDT),
			humanUSD(bybitLongVolUSDT), humanUSD(bybitShortVolUSDT),
			bybitCount, humanUSD(bybitBiggestUSDT), bybitBiggestSide,
			bybitMin, bybitMax,
			curBybitFunding*100, humanUSD(curBybitOI), signedUSD(bybitOIDelta),
		)

		log.Printf("[ENGINE] Dispatching Telegram alert (combined impact %s)...", humanUSD(totalImpactUSDT))
		telegram.DispatchTelegramAlert(token, chatID, msg)
	}
}
