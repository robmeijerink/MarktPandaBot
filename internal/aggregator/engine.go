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

// flushWindowCVD returns the net signed perp taker notional (USD) around the flush:
// the just-closed UTC 5-minute bucket plus the in-progress one. Summing both makes
// it robust to where the engine's (unaligned) evaluation tick falls relative to the
// UTC bucket boundary, capturing the ~5–10 minutes of taker flow during the cascade.
// Positive => net taker-buying (absorption under a long flush).
func flushWindowCVD(flow *FlowTracker, now time.Time) float64 {
	cur := floorTo5Min(now)
	return flow.PerpCVD(cur) + flow.PerpCVD(cur.Add(-EvaluationInterval))
}

func RunConfluenceEngine(aggregator *Aggregator, state *MarketState, cfg Config, ring *VolumeRing, confMgr *ConfirmationManager, flow *FlowTracker, outcome *OutcomeLogger, token, chatID string) {
	ticker := time.NewTicker(EvaluationInterval)
	defer ticker.Stop()

	log.Printf("[ENGINE] Confluence engine started. Evaluating every %s "+
		"(dynamic threshold, floor %s; OI labels Potential at >= %.2f%%, Likely at >= %.2f%%)",
		EvaluationInterval, humanUSD(ThresholdFloorUSDT), MinOISignalFraction*100, StrongOISignalFraction*100)

	// Trailing histories for the adaptive thresholds and the funding-trend test.
	// They are fed EVERY cycle (including idle ones) so the distributions stay
	// representative of the regime rather than only of alert windows.
	fundingLookbackWindows := int(cfg.FundingLookbackHours * 3600 / EvaluationInterval.Seconds())
	if fundingLookbackWindows < 1 {
		fundingLookbackWindows = 1
	}
	oiDropHist := NewStatRing(cfg.BufferSize)              // recent OI-drop magnitudes (%)
	cvdHist := NewStatRing(cfg.BufferSize)                 // recent |flush-window perp CVD| (USD)
	fundingHist := NewStatRing(fundingLookbackWindows + 1) // funding values for the lookback read

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
		hadBaseline := oiBaseline // false only on the very first cycle
		oiBaseline = true

		// OI heartbeat: logs every cycle (incl. idle) so a frozen OI feed is
		// obvious. OKX's open-interest channel pushes ~every 3s, so its Δ should
		// rarely be exactly $0 — a persistent "Δ +$0" here means the feed is stale,
		// not that the market is flat. (Funding moves slowly, so its small Δ is
		// normal.) The first cycle has no baseline yet, so it is not flagged.
		oiStale := ""
		if hadBaseline && okxOIDelta == 0 && bybitOIDelta == 0 {
			oiStale = "  ⚠️ both OI deltas exactly $0 — check OI feeds"
		}
		log.Printf("[ENGINE] OI heartbeat: OKX %s (Δ %s) | Bybit %s (Δ %s) | funding OKX %.4f%% Bybit %.4f%%%s",
			humanUSD(curOKXOI), signedUSD(okxOIDelta), humanUSD(curBybitOI), signedUSD(bybitOIDelta),
			curOKXFunding*100, curBybitFunding*100, oiStale)

		// Feed the trailing histories EVERY cycle so the adaptive percentiles and the
		// funding-trend lookback reflect the whole regime. combinedOI/Δ and the
		// flush-window perp CVD are available without the liquidation aggregation.
		combinedOI := curOKXOI + curBybitOI
		combinedOIDelta := okxOIDelta + bybitOIDelta
		oiChangePct := 0.0
		if combinedOI > 0 {
			oiChangePct = combinedOIDelta / combinedOI * 100
		}
		flushCVD := flushWindowCVD(flow, time.Now())
		oiDropHist.Add(math.Max(0, -oiChangePct)) // drop magnitude in %
		cvdHist.Add(math.Abs(flushCVD))
		fundingHist.Add(curBybitFunding)

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
		// combinedOI / combinedOIDelta / oiChangePct were computed above (fed to the
		// history rings every cycle); reuse them here.
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

		// Title + signal stay outside the code fence so bold/emoji render; the
		// per-venue tables go inside one ``` block so every column lines up.
		msg := fmt.Sprintf(
			"🚨 *LIQUIDATION ALERT*\n\n"+
				"*%s*\n\n"+
				"📈 OI %+.2f%%  ·  BTC $%s  ·  %+.1f%% 24h\n\n"+
				"⚠️ Combined ~%s liquidated in the last 5m\n\n"+
				"```\n%s\n\n%s\n```",
			signalLabel, oiChangePct, comma(bybitLast), bybitPctDisplay,
			humanUSD(totalImpactUSDT),
			formatExchangeBlock("📍", "BYBIT", bybit, curBybitFunding, curBybitOI, bybitOIDelta),
			formatExchangeBlock("🌐", "OKX", okx, curOKXFunding, curOKXOI, okxOIDelta),
		)

		// ─── Two-stage scoring injection (upgrade.md §4–§6) ──────────────────
		// Single, additive hook into the existing alert path. Everything above is
		// untouched (APPEND ONLY, rule 1 / D2): the existing logic already decided
		// to alert; here we only enrich it with the T0 Setup Matrix and, on a
		// qualifying score, start the cancelable T+N candle-sync confirmation.
		bucketVol, _ := ring.Latest()
		medianVol, medianOK := ring.Median()

		// Adaptive references from the trailing histories (fed every cycle above).
		volRank, volRankOK := ring.Rank(bucketVol)
		oiDropRank, oiDropRankOK := oiDropHist.Rank(math.Max(0, -oiChangePct), cfg.AdaptiveMinSamples)
		cvdRank, cvdRankOK := cvdHist.Rank(math.Abs(flushCVD), cfg.AdaptiveMinSamples)
		fundingPrev, fundingHasHist := fundingHist.Ago(fundingLookbackWindows)
		longsDominant := combinedLongUSDT >= combinedShortUSDT

		score := ScoreSetup(cfg, SetupInputs{
			OIChangePct:  oiChangePct,
			LongLiqUSDT:  combinedLongUSDT,
			ShortLiqUSDT: combinedShortUSDT,
			FundingRate:  curBybitFunding, // PrimaryExchange (Bybit) funding
			BucketVol:    bucketVol,
			MedianVol:    medianVol,
			VolMedianOK:  medianOK,

			VolPctileRank:    volRank,
			VolRankOK:        volRankOK,
			OIDropPctileRank: oiDropRank,
			OIDropRankOK:     oiDropRankOK,

			PerpCVD:       flushCVD,
			PerpActive:    flow.PerpActive(),
			LongsDominant: longsDominant,
			CVDPctileRank: cvdRank,
			CVDRankOK:     cvdRankOK,

			FundingPrev:       fundingPrev,
			FundingHasHistory: fundingHasHist,
		})
		msg += FormatSetupMatrix(cfg, score)

		// Log the RAW value behind each signal next to its threshold, so the gate
		// can be calibrated from the real distribution of events rather than
		// guessed. "vol" is n/a until the ring has MinBufferFill samples.
		longShare := longLiqShare(combinedLongUSDT, combinedShortUSDT)
		volRatio := 0.0
		volRatioStr := "n/a(warming)"
		if medianOK && medianVol > 0 {
			volRatio = bucketVol / medianVol
			volRatioStr = fmt.Sprintf("%.1fx", volRatio)
		}
		log.Printf("[ENGINE] T0 signals: score %d/%d | "+
			"OI Δ %.2f%% (oiRank %.2f) %s | skew %.1f%% (bar ≥%.0f) %s | "+
			"vol %s (volRank %.2f) %s | funding %.4f%% (prev %.4f%%) %s | "+
			"perpCVD %s (cvdRank %.2f, longsDom %t) %s",
			score.Total, score.Max,
			oiChangePct, oiDropRank, passMark(score.OIDrop),
			longShare, cfg.SkewPct, passMark(score.Skew),
			volRatioStr, volRank, passMark(score.VolSpike),
			curBybitFunding*100, fundingPrev*100, passMark(score.Funding),
			signedUSD(flushCVD), cvdRank, longsDominant, cvdMark(score))

		log.Printf("[ENGINE] Dispatching Telegram alert (combined impact %s)...", humanUSD(totalImpactUSDT))
		telegram.DispatchTelegramAlert(token, chatID, msg)

		// Outcome logging (#4): label this alert and measure its forward return so
		// signal accuracy can be learned from real events. Reversal direction
		// follows the flush side (long flush => reversal UP).
		if outcome != nil && cfg.OutcomeLogEnabled {
			outcome.Record(OutcomeSnapshot{
				T0:            time.Now().UTC(),
				BaselinePrice: bybitLast,
				ReversalUp:    longsDominant,
				Score:         score,
				OIChangePct:   oiChangePct,
				LongSharePct:  longShare,
				VolRatio:      volRatio,
				FundingPct:    curBybitFunding,
				PerpCVD:       flushCVD,
			})
		}

		// D3: confirmation only starts for meaningful (absolute-score) setups. A
		// fresh qualifying T0 cancels any pending confirmation (§6.1).
		if score.QualifiesForConfirmation(cfg) {
			log.Printf("[ENGINE] T0 score %d >= gate %d — starting candle-sync confirmation.",
				score.Total, cfg.StartConfirmationMinScore)
			confMgr.Trigger(T0Snapshot{
				FlushRangeHigh: bybit.max, // liquidation range top on PrimaryExchange (Bybit)
				BaselinePrice:  bybitLast,
				T0:             time.Now().UTC(),
			})
		}
	}
}
