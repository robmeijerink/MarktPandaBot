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

	// --- Dynamic liquidation threshold (Bybit leader, USDT) ---
	// threshold = max(ThresholdFloorUSDT, regimeBaseline * volatilityMultiplier)
	// regimeBaseline = VolumeBaselineFraction * (Bybit 24h $ turnover / windows).
	// Calibrated to live data (~2026-06-02): Bybit turnover ~$8.95B/24h => a
	// 5-min slice ~$31M, so 0.011 puts the baseline ~$340k ≈ the floor on a
	// busy day. That makes volMult actually matter — on calm days the floor
	// governs; on busy/volatile days the bar scales above it so only standout
	// cascades pass. (At 0.004 the baseline was ~$124k and never beat the floor,
	// i.e. the threshold was a flat $350k regardless of regime.)
	ThresholdFloorUSDT     = 350000.0 // hard floor; never alert below this
	VolumeBaselineFraction = 0.011    // ~1.1% of one window's share of 24h $ turnover
	VolatilityRefRange     = 0.03     // 3% normalised 24h range == "normal" (multiplier 1.0)
	MaxVolatilityMultiple  = 3.0      // cap how far volatility can raise the bar

	// OKX is the CONFIRM leg, not the leader, and its BTC liquidation volume is
	// a small, highly variable share of Bybit's (logs: ~2% in a long
	// capitulation, ~68% in a short squeeze), so a fraction-of-the-leader bar is
	// unreliable. Instead OKX just has to show genuine corroborating activity: a
	// flat floor set from live data — dead-quiet windows top out ~$10k of OKX
	// BTC liquidations, while real cascades ran $114k and $272k, so $50k (5x the
	// noise ceiling) cleanly separates "OKX really participated" from background
	// without vetoing moderate moves. Going higher wouldn't help (capitulations
	// are bimodal: huge or near-noise) and would only filter legit squeezes.
	// Magnitude filtering is the leader's job; this only answers "market-wide?".
	OKXConfirmFloorUSDT = 50000.0

	// --- OI signal classification (NOT a filter) ---
	// Every alert that clears the volume threshold is sent; the open-interest
	// flow only labels it. A directional call (REVERSAL when OI falls,
	// CONTINUATION when OI rises) requires |OI change| >= this over the window;
	// anything smaller is labelled "Unclear". Kept deliberately strict so a
	// reversal isn't called on a minor OI wobble — raise for stricter, lower for
	// more eager labels.
	MinOISignalFraction = 0.007 // 0.7% of combined OI over the 5-min window
	// A larger swing upgrades the label from "Potential" to "Likely". Real
	// events seen: a 2.13% drop (capitulation) vs a 0.96% rise (continuation),
	// so 1.5% cleanly separates a strong conviction call from a borderline one.
	StrongOISignalFraction = 0.015 // 1.5% of combined OI => "Likely"
)

// windowsPerDay is how many evaluation windows fit in 24h (288 for 5-min).
var windowsPerDay = (24 * time.Hour).Seconds() / EvaluationInterval.Seconds()

// MarketState holds the real-time background data updated by secondary
// WebSockets. OI values are in USD on both legs so they are comparable.
type MarketState struct {
	Mu               sync.RWMutex
	OKXFunding       float64
	OKXOI            float64 // USD (oiUsd)
	BybitFunding     float64
	BybitOI          float64 // USD (openInterestValue)
	BybitLastPrice   float64
	BybitPct24h      float64 // 24h change as a fraction
	BybitHigh24h     float64
	BybitLow24h      float64
	BybitTurnover24h float64 // 24h turnover in USD (Bybit leads the threshold)
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

// classifySide reports whether a NORMALISED order-side string is a long or a
// short liquidation: SELL = a long was liquidated, BUY = a short was liquidated.
// Every feed must already be in this convention before it reaches the engine —
// OKX/Binance natively are; Bybit (which reports POSITION side) is inverted by
// bybit.normaliseSide. See [[liquidation-side-conventions]].
func classifySide(side string) (isLong, isShort bool) {
	return side == "SELL" || side == "Sell", side == "BUY" || side == "Buy"
}

// legStats holds the aggregated liquidation stats for one exchange in a window.
type legStats struct {
	count       int
	volBTC      float64 // total BTC liquidated
	volUSDT     float64
	longUSDT    float64
	shortUSDT   float64
	min, max    float64
	biggestUSDT float64
	biggestSide string
}

func (l *legStats) add(valueUSDT, price float64, isLong, isShort bool) {
	if isLong {
		l.longUSDT += valueUSDT
	} else if isShort {
		l.shortUSDT += valueUSDT
	}
	if valueUSDT > l.biggestUSDT {
		l.biggestUSDT = valueUSDT
		l.biggestSide = sideLabel(isLong, isShort)
	}
	if l.count == 0 || price < l.min {
		l.min = price
	}
	if l.count == 0 || price > l.max {
		l.max = price
	}
	l.volUSDT += valueUSDT
	l.count++
}

// computeStats aggregates a window of liquidation events per exchange. Buckets,
// biggest-single, and totals all derive from the same classifySide call, so the
// per-leg longs/shorts, the biggest label, and (downstream) the reversal
// direction can never disagree.
func computeStats(data []LiquidationEvent) (okx, bybit legStats) {
	// Both feeds store Qty in BTC; USD is derived here as Qty*Price.
	for _, item := range data {
		isLong, isShort := classifySide(item.Side)
		valueUSDT := item.Qty * item.Price
		switch item.Exchange {
		case "okx":
			okx.volBTC += item.Qty
			okx.add(valueUSDT, item.Price, isLong, isShort)
		case "bybit":
			bybit.volBTC += item.Qty
			bybit.add(valueUSDT, item.Price, isLong, isShort)
		}
	}
	return okx, bybit
}

// dynamicThreshold returns the per-leg USDT threshold for the current market
// regime, plus the volatility multiplier used (for logging). turnover24hUSD,
// price and 24h range come from the leader venue (Bybit).
func dynamicThreshold(turnover24hUSD, lastPrice, high24h, low24h float64) (threshold, volMult float64) {
	// One evaluation window's share of 24h $ turnover.
	windowVolumeUSD := turnover24hUSD / windowsPerDay
	baseline := windowVolumeUSD * VolumeBaselineFraction

	volRange := 0.0
	if lastPrice > 0 {
		volRange = (high24h - low24h) / lastPrice
	}
	volMult = 1.0
	if VolatilityRefRange > 0 {
		volMult = volRange / VolatilityRefRange
	}
	if volMult < 1.0 {
		volMult = 1.0
	}
	if volMult > MaxVolatilityMultiple {
		volMult = MaxVolatilityMultiple
	}

	threshold = baseline * volMult
	if threshold < ThresholdFloorUSDT {
		threshold = ThresholdFloorUSDT
	}
	return threshold, volMult
}

// classifySignal labels a liquidation burst from combined OI flow and the
// dominant liquidation side. OI falling => positions flushed and not replaced
// (capitulation / squeeze) => reversal; OI rising => fresh positions =>
// continuation; OI roughly flat => unclear. The size of the swing sets the
// confidence: "Potential" past MinOISignalFraction, "Likely" past
// StrongOISignalFraction. dropFraction is positive when OI fell. This is a
// LABEL, not a gate — every alert is sent regardless.
func classifySignal(combinedOI, combinedOIDelta, longUSDT, shortUSDT float64) (label string, dropFraction float64) {
	if combinedOI > 0 {
		dropFraction = -combinedOIDelta / combinedOI // positive when OI fell
	}
	longsDominant := longUSDT >= shortUSDT
	mag := math.Abs(dropFraction)

	// Below the signal bar: not enough OI movement to call a direction.
	if mag < MinOISignalFraction {
		if longsDominant {
			return "❓ Unclear — longs flushed, OI flat", dropFraction
		}
		return "❓ Unclear — shorts flushed, OI flat", dropFraction
	}

	conf := "Potential"
	if mag >= StrongOISignalFraction {
		conf = "Likely"
	}

	if dropFraction > 0 { // OI falling => reversal
		if longsDominant {
			return fmt.Sprintf("🔄 %s REVERSAL UP — long capitulation", conf), dropFraction
		}
		return fmt.Sprintf("🔄 %s REVERSAL DOWN — short squeeze", conf), dropFraction
	}
	// OI rising => continuation
	if longsDominant {
		return fmt.Sprintf("➡️ %s CONTINUATION DOWN — longs flushed, OI rising", conf), dropFraction
	}
	return fmt.Sprintf("➡️ %s CONTINUATION UP — shorts flushed, OI rising", conf), dropFraction
}

func RunConfluenceEngine(aggregator *Aggregator, state *MarketState, token, chatID string) {
	ticker := time.NewTicker(EvaluationInterval)
	defer ticker.Stop()

	log.Printf("[ENGINE] Confluence engine started. Evaluating every %s "+
		"(dynamic threshold, floor %s; OI labels Potential at >= %.2f%%, Likely at >= %.2f%%)",
		EvaluationInterval, humanUSD(ThresholdFloorUSDT), MinOISignalFraction*100, StrongOISignalFraction*100)

	// OI baselines for the 5-minute delta, carried across cycles.
	var prevOKXOI, prevBybitOI float64
	var oiBaseline bool

	for range ticker.C {
		// Snapshot background context every cycle (incl. idle ones) so the OI
		// delta always reflects the full window when an alert eventually fires.
		state.Mu.RLock()
		curOKXFunding := state.OKXFunding
		curOKXOI := state.OKXOI
		curBybitFunding := state.BybitFunding
		curBybitOI := state.BybitOI
		// Bybit is the leader venue: it drives the regime threshold and the
		// price/24h context line.
		bybitLast := state.BybitLastPrice
		bybitPct24h := state.BybitPct24h
		bybitHigh24h := state.BybitHigh24h
		bybitLow24h := state.BybitLow24h
		bybitTurnover24h := state.BybitTurnover24h
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

		okx, bybit := computeStats(data)

		// Dynamic, regime-aware threshold (same for both legs), driven by the
		// leader venue (Bybit, the deeper liquidation market).
		threshold, volMult := dynamicThreshold(bybitTurnover24h, bybitLast, bybitHigh24h, bybitLow24h)
		isPrimaryTrigger := bybit.volUSDT >= threshold    // Bybit (leader) full bar
		isConfirmed := okx.volUSDT >= OKXConfirmFloorUSDT // OKX shows corroborating activity

		// OI signal: labels the burst as reversal / continuation / unclear.
		combinedOI := curOKXOI + curBybitOI
		combinedOIDelta := okxOIDelta + bybitOIDelta
		combinedLongUSDT := okx.longUSDT + bybit.longUSDT
		combinedShortUSDT := okx.shortUSDT + bybit.shortUSDT
		signalLabel, oiDropFrac := classifySignal(combinedOI, combinedOIDelta, combinedLongUSDT, combinedShortUSDT)

		log.Printf("[ENGINE] Threshold=%s (floor %s x volMult %.2f) | Bybit %s (trig=%t) | OKX %s vs confirm %s (conf=%t)",
			humanUSD(threshold), humanUSD(ThresholdFloorUSDT), volMult,
			humanUSD(bybit.volUSDT), isPrimaryTrigger, humanUSD(okx.volUSDT), humanUSD(OKXConfirmFloorUSDT), isConfirmed)
		log.Printf("[ENGINE] Signal -> combined OI %s, Δ %s (drop %.2f%%) [%s]",
			humanUSD(combinedOI), signedUSD(combinedOIDelta), oiDropFrac*100, signalLabel)

		// Diagnostic: warn when one leg is silent while the other has volume.
		if okx.count > 0 && bybit.count == 0 {
			log.Println("[ENGINE] WARNING: OKX has liquidations but Bybit feed is EMPTY this cycle — check the Bybit stream.")
		} else if bybit.count > 0 && okx.count == 0 {
			log.Println("[ENGINE] WARNING: Bybit has liquidations but OKX feed is EMPTY this cycle — check the OKX stream.")
		}

		if !isPrimaryTrigger {
			log.Printf("[ENGINE] No alert: Bybit (leader) %s below threshold %s.",
				humanUSD(bybit.volUSDT), humanUSD(threshold))
			continue
		}
		if !isConfirmed {
			log.Printf("[ENGINE] No alert: OKX did not corroborate (%s < confirm floor %s).",
				humanUSD(okx.volUSDT), humanUSD(OKXConfirmFloorUSDT))
			continue
		}

		log.Printf("[ENGINE] ALERT TRIGGERED: volume thresholds met. Signal: %s. Building message...", signalLabel)

		totalImpactUSDT := okx.volUSDT + bybit.volUSDT
		bybitPctDisplay := bybitPct24h * 100 // price24hPcnt is a fraction
		oiChangePct := 0.0
		if combinedOI > 0 {
			oiChangePct = combinedOIDelta / combinedOI * 100
		}

		msg := fmt.Sprintf(
			"🚨 *LIQUIDATION ALERT*\n\n"+
				"*%s* (OI %+.2f%%)\n\n"+
				"_⚠️ Combined (Bybit & OKX): ~%s liquidated in the past 5 minutes._\n"+
				"📊 BTC $%.0f (%+.1f%% 24h)\n\n"+
				"📍 *BYBIT* (Total: ~%s / %.2f ₿)\n"+
				"🔴 Longs: ~%s   🟢 Shorts: ~%s\n"+
				"Ord: %d   Biggest: ~%s %s\n"+
				"Rng: %.0f - %.0f\n"+
				"Fund: %.4f%%   OI: %s (Δ %s)\n\n"+
				"🌐 *OKX* (Total: ~%s / %.2f ₿)\n"+
				"🔴 Longs: ~%s   🟢 Shorts: ~%s\n"+
				"Ord: %d   Biggest: ~%s %s\n"+
				"Rng: %.0f - %.0f\n"+
				"Fund: %.4f%%   OI: %s (Δ %s)",
			signalLabel, oiChangePct,
			humanUSD(totalImpactUSDT),
			bybitLast, bybitPctDisplay,
			humanUSD(bybit.volUSDT), bybit.volBTC,
			humanUSD(bybit.longUSDT), humanUSD(bybit.shortUSDT),
			bybit.count, humanUSD(bybit.biggestUSDT), bybit.biggestSide,
			bybit.min, bybit.max,
			curBybitFunding*100, humanUSD(curBybitOI), signedUSD(bybitOIDelta),
			humanUSD(okx.volUSDT), okx.volBTC,
			humanUSD(okx.longUSDT), humanUSD(okx.shortUSDT),
			okx.count, humanUSD(okx.biggestUSDT), okx.biggestSide,
			okx.min, okx.max,
			curOKXFunding*100, humanUSD(curOKXOI), signedUSD(okxOIDelta),
		)

		log.Printf("[ENGINE] Dispatching Telegram alert (combined impact %s)...", humanUSD(totalImpactUSDT))
		telegram.DispatchTelegramAlert(token, chatID, msg)
	}
}
