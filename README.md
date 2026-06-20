# 🐼 MarktPandaBot: Liquidation & Context Tracker

A stateful BTCUSDT Telegram alert system designed to detect genuine Support/Resistance (S/R) breakouts by analyzing cryptocurrency liquidation clusters, Open Interest (OI), and Funding Rates.

Most liquidation bots spam your feed with every single forced order, leading to severe alert fatigue. This tracker solves that by acting as a high-pass filter: it aggregates live market data across multiple WebSockets and only notifies you when a significant, multi-exchange market shift occurs, complete with the underlying market context.

## 📡 Live Telegram Channel

Don't want to deal with Go environments, CI/CD pipelines, or API limits? You can use the MarktPanda Bot live for FREE.

Join the public Telegram channel to instantly receive:
- Real-time combined OKX & Bybit liquidation alerts
- Clear visual breakdowns of Long vs. Short liquidations
- Market funding rates & Open Interest metrics

🔗 **[Join the official MarktPanda Channel](https://t.me/marktpanda)**

## 🎯 What it is

The Liquidation Confluence Tracker is an automated, concurrent market monitor written in Go. It watches for massive liquidation events in the crypto futures market (specifically BTC/USDT). Instead of forwarding raw data, it groups liquidations into 5-minute time windows and evaluates them against configurable volume thresholds.

If the liquidations indicate a true market exhaustion or a massive breakout, it pushes a highly condensed, easily scannable alert directly to your Telegram or Smartwatch, enriched with real-time Open Interest changes and Funding Rate data to validate the market's true direction.

On top of that base alert, an optional **two-stage scoring layer** grades each event the moment it fires and — for the strongest setups only — follows up a few minutes later with a candle-synced confirmation. This turns a "something happened" notification into a "here's how convincing it was, and whether the market actually reversed" workflow, while keeping low-conviction events quiet.

## ⚙️ How it Works

The core of this tracker is built on a **Stateful Confluence Strategy** (Global Truth + Local Confirmation + Context). It simultaneously maintains concurrent WebSocket connections to two major derivatives exchanges:

1. **Exchange One (OKX):** The tracker hooks into the public liquidation and ticker streams. If massive liquidation clusters occur on OKX (crossing the configured evaluation thresholds within 5 minutes), it signals major algorithmic execution.

2. **Exchange Two (Bybit):** Bybit provides the secondary confirmation via its `allLiquidation` and `tickers` streams. A market move is only actionable if validated by Bybit volume concurrently. Furthermore, both exchanges provide crucial real-time **Open Interest** and delta changes to see if new money is aggressively entering or leaving the market during the liquidation cascade.

### The Alert Lifecycle

1. **Listen:** Goroutines silently collect real-time forced orders, while parallel workers continuously update a shared `MarketState` (protected by a Read-Write Mutex) with the latest Open Interest and Funding Rates from both OKX and Bybit.
2. **Aggregate:** Every 5 minutes, the engine calculates the total liquidated volume (normalized to USDT/USD), order count, the biggest single liquidation print, and the exact price range (slippage) of those liquidations.
3. **Evaluate:** It checks if the aggregated volume crosses the configured confluence thresholds for *both* exchanges simultaneously.
4. **Notify:** If confluence is achieved, the bot safely reads the latest OI and Funding contexts, formats a minimalist, smartwatch-optimized alert, and dispatches it via the Telegram API.
5. **Score (T0):** The same alert is graded by the Setup Matrix and the score is appended to the message (see below).
6. **Confirm (T+N):** For high-conviction setups only, a cancelable goroutine waits for the next fully-closed candle and sends an independent follow-up message reporting whether the move was confirmed, absorbed, or rejected.

### Two-Stage Scoring Layer

The scoring layer is **purely additive** — it never alters or suppresses the base liquidation alert, it only enriches it. Everything it does is driven by a single configuration block, so weights and thresholds can be re-tuned without touching the engine logic.

**Stage 1 — Setup Matrix (T0, immediate).** When the base alert fires, a handful of confluence signals are scored and the result is appended to the alert as a compact matrix with a total score and a conviction label. The signals are read from data the alert already has, plus a rolling trading-volume baseline:

- **OI Drop** — open interest falling over the window (positions flushed, not replaced).
- **Skew** — share of long vs. short liquidations, to detect one-sided capitulation.
- **Vol Spike** — current trading volume vs. a rolling baseline of recent 5-minute buckets.
- **Funding** — funding at/through the neutral line **or** a sharp downtrend from a positive level (longs capitulating, anticipating the flip).
- **CVD** — perp cumulative-volume-delta (net taker flow) **opposing** the flush: buyers stepping in under a long capitulation, or sellers into a short squeeze. This is the absorption confirmation, and it's the one signal that's independent of the position-based items above, so it adds genuinely new information rather than echoing them.

Each signal carries a configurable weight; the totals and the conviction cutoff are all tunable.

**Adaptive thresholds.** Rather than fixed cutoffs (e.g. "5× median volume", "0.7% OI drop") that drift out of calibration as the market regime changes, OI Drop, Vol Spike, and CVD can be gated on their **trailing distribution** — a signal passes when the current reading ranks in the top percentile of recent windows. Each adaptive gate falls back to its fixed bar until its history ring has warmed up, so the score is always defined. Toggle with `UseAdaptiveThresholds`.

**Measuring accuracy.** None of these signals are validated edges. Every dispatched alert is labelled with its full feature vector (`[OUTCOME-T0]`) and its realised forward return at 15/30/60 minutes (`[OUTCOME-FWD]`), as structured log lines joined by an `id=`. Grep the logs and you can compute the real hit-rate of each signal and each combination, then re-weight the matrix from data instead of intuition.

**Stage 2 — Candle-Sync Confirmation (T+N, delayed).** Only setups at or above the configured conviction cutoff start a confirmation. A cancelable worker aligns to UTC 5-minute boundaries, waits for the next fully-closed candle, and then reports a verdict from three signals:

- **Price Reclaim** — does the candle close back above the liquidation range high? (required)
- **CVD Inflow** — net signed taker volume on the perpetual trade streams.
- **Spot vs. Perp** — net spot taker-buying absorbing the perp flush.

A fresh qualifying alert cancels any pending confirmation and starts a new one, so the feed never carries stale follow-ups. The current build targets a single direction (long capitulation → reversal up); the mirror case is intentionally out of scope.

**Warm boot.** On startup, the rolling volume baseline is hydrated from public REST kline history before any WebSocket opens, so the Vol Spike signal is meaningful from the first cycle. Hydration is best-effort: if it fails it simply warms up from live data instead, and never blocks or crashes startup.

> ⚠️ **The default weights and thresholds are reasonable starting points, not a validated edge.** They should be backtested against historical flushes before the scores and verdicts are traded on. Because every value lives in one config block, that tuning needs no code changes.

## 📐 21/200 SMA Retest Alerts (Independent Module)

A separate, fully self-contained module watches for **pullback / retest entries** on the 3-minute timeframe. It is completely decoupled from the liquidation engine above — it keeps its own state, streams its own candles, and sends its own messages. It shares no data with the scoring layer and cannot affect the base liquidation alerts.

The idea is mechanical, not pattern-matching, and follows the CryptoLifer "model": a 21/200 SMA cross sets the trend, the module waits for a **real directional impulse (flagpole)** that the market then consolidates into a **tight, contracting flag**, and only then takes the **bar-close touch of the 21 SMA** (dynamic support for longs, resistance for shorts) as the entry confirmation. A pullback all the way to the **200 SMA** invalidates the setup.

**How it works:**

1. **Trend (regime).** A golden cross (21 SMA above 200 SMA) arms **long** retest setups; a death cross arms **short** setups. Both directions are watched. There is no alert on the cross itself — only on the subsequent confirmed entry.
2. **Flagpole gate.** A touch only counts as an entry when a **real, trend-aligned impulse precedes the consolidation** — the pole must be a genuine directional move (close-to-close ≥ `FlagMinPolePct` of price over `PoleLookback` bars) and it must dominate the consolidation (≥ `FlagMinPoleRatio` × flag range). The move must run **with** the trend (up into a long, down into a short). Without this gate, quiet sideways chop passes the tightness test and fires on every mean reversion; the pole requirement enforces that only a real impulse followed by digestion (flag) triggers the entry. The gate can be disabled with `RequireTightFlag=false`.
3. **Tight-flag gate.** The consolidation leading into the touch must be a **tight, contracting flag** — over a lookback window the recent range must be small (within `FlagMaxRangePct` of price) and tighter than the earlier range (`FlagContractionRatio`). This ensures the market has digested the impulse and is coiling before the entry touch, matching the CryptoLifer model timing. Both the pole and the tight flag are required; either can fail independently.
4. **Bar-close touch.** A long touch fires only when the candle's low reaches the 21 SMA (within a small tolerance band) **and the candle closes back at or above it** — a wick that pierces the SMA but closes below is rejected. Shorts mirror this. This close-on-the-right-side filter is the whole point of evaluating on closed bars.
5. **Invalidation.** If a pullback reaches the 200 SMA, the setup is disarmed until the next cross. (An optional note can be emitted when this happens.)
6. **Anti-spam.** By default a single entry is alerted per regime (first-only) — one notification per cross, then silent until the next 21/200 cross. An optional debounce mode instead allows repeat touches, but only after price has closed back outside the tolerance band between them.
7. **Warm boot.** On startup the module silently hydrates ~300 closed 3m candles from REST and establishes the current regime **without firing a historical alert**; the first qualifying live bar can still trigger.

**Data source.** The 3m candles come from the primary exchange (Bybit perp BTC/USDT by default) over a WebSocket kline subscription, with a REST poll as an automatic fallback if the socket goes quiet — so a dropped connection or a geo-blocked REST host (it transparently fails over to Bybit's `bytick.com` mirror) does not silence the feed.

> ⚠️ **3m is fast and noisy, and this is plumbing for a setup signal — not a validated edge.** Expect more alerts than on higher timeframes, and note that runaway trends that never retest the 21 SMA are missed by design. Backtest the long and short legs separately before acting on them. Every threshold lives in the module's config block.

## ✨ Key Features

- **Zero Alert Fatigue:** 5-minute rolling windows and configurable volume confluence filters ensure you only get notified during major volatility blocks.
- **Two-Stage Conviction Scoring:** Every alert is graded by a tunable Setup Matrix, and only high-conviction setups trigger a delayed, candle-synced confirmation message — separating "a flush happened" from "the flush actually reversed."
- **21/200 SMA Retest Module:** A fully independent add-on that watches 3-minute candles for bar-close retests of the 21 SMA after a 21/200 cross (both long and short), with a 200-SMA invalidation guard, anti-spam debounce, and a WebSocket-primary / REST-fallback candle feed.
- **Stateful Context Engine:** Doesn't just report the crash; it reports the context. Real-time Open Interest shifts ($\Delta$) and Funding Rates are attached to every alert to help identify Short Squeezes, long-squeezes, and trap setups.
- **Smartwatch Optimized:** Alerts are meticulously formatted using minimalist layouts, specific bold markers, and clean line breaks, allowing you to read Volume, Range, Funding, and OI delta at a single glance on your wrist.
- **DevOps Ready:** Compiled as a 100% statically linked Linux binary (`CGO_ENABLED=0`). Extremely lightweight footprint (~30MB RAM), perfect for hosting on cloud resources or micro-instances like a worker node.

## 📱 Alert Format Example

```markdown
🚨 LIQUIDATION ALERT
⚠️ Combined (OKX & Bybit): ~$420k liquidated in the past 5 minutes.
📊 BTC $71590 (-2.6% 24h)

🌐 OKX (Total: ~$293k / 4.10 ₿)
🔴 Longs: ~$272k   🟢 Shorts: ~$21k
Ord: 12   Biggest: ~$61k long
Rng: 71400 - 71800
Fund: 0.0100%   OI: $2.56B (Δ +$12.3M)

📍 BYBIT (Total: ~$240k)
🔴 Longs: ~$210k   🟢 Shorts: ~$30k
Ord: 8   Biggest: ~$40k long
Rng: 71390 - 71810
Fund: 0.0120%   OI: $4.29B (Δ -$5.1M)

------------------------
📊 SETUP MATRIX (T0)
✅ OI Drop     top 10% drop   +<weight>
✅ Skew        ≥90% long      +<weight>
❌ Vol Spike   top 10% vol    0
✅ Funding     flip/trend ≤0  +<weight>
✅ CVD         absorb flush   +<weight>

Score: <sum>/<max>   (HIGH CONVICTION)
⏳ Monitoring absorption window…
```

For high-conviction setups, an independent follow-up arrives once the next candle closes:

```markdown
✅ CONFIRMATION (T+5m, candle 14:05 UTC)
Pair: BTC/USDT | Close: $71840.00
📊 Confirmation Matrix:
✅ Price Reclaim (> $71810.00)
✅ CVD Inflow (net positive)
⚪ Spot vs Perp (spot leading)

Verdict: Reversal confirmed
```

> The exact thresholds shown in the matrix labels and the score denominator are rendered from your configuration at runtime; the values above are illustrative. A `⚪` marks a signal that was unavailable and excluded from the verdict.

The independent SMA retest module sends its own message, with a distinct `📐` prefix so it stays readable in the same feed:

```markdown
📐 SMA RETEST — LONG (3m)
BTC/USDT  @ 63704.40
21 SMA: 63702.10   |   200 SMA: 63180.50
Touch low: 63689.20   (stop reference, not advice)
Room to 200 SMA: 0.82%
Pole: +0.95% impulse over 6 bars
Flag: tight (0.31% over 12 bars)
Regime: 14 bars since golden cross
Pole + tight-flag touch of the 21 SMA (support held) — model entry.
```

> Short setups mirror this (death cross, `Touch high`, "resistance held"). The pole shows the signed directional impulse; the flag shows the tight consolidation range. The values above are illustrative.

## 🚀 Setup & Configuration

### Prerequisites

- Docker (Colima/Orbstack for macOS or native Linux Docker engine)

- Taskfile (`go-task`)

- Go 1.26+ (configured via toolchain tool)


### Environment Variables

To run this tracker, you must provide the following environment variables to the service context:

- `TELEGRAM_BOT_TOKEN`: The API token provided by Telegram's BotFather.

- `TELEGRAM_CHAT_ID`: The ID of your public or private channel/chat (typically starts with `-100`).


### Local Testing

Run the bot interactively using your defined Taskfile definitions:

Bash

```
task run
```

### Production Build

Compile the static Linux binary for deployment:

Bash

```
task build
```

This generates the static `marktpanda_bot` executable, which can be deployed directly to your server instance via SCP/RSYNC and run as a standard Systemd service.

### Tuning

All tunable behavior lives in configuration blocks inside the internal packages — no engine logic needs to change to adjust it:

- **Confluence thresholds** for the base liquidation alert (per-exchange volume gating, regime sensitivity).
- **Setup Matrix** signal weights and pass thresholds (incl. the CVD absorption floor and the adaptive percentile gates), the funding flip/trend lookback, the conviction cutoff that gates the follow-up confirmation, and the outcome-logging horizons.
- **Confirmation timing** (candle interval and minimum lead time) and **warm-boot** parameters (history depth, fetch timeout/retries).
- **SMA Retest module** (separate config block): timeframe and SMA periods, the 21-SMA touch tolerance (percent band or ATR-based), the flagpole gate (`PoleLookback`, `FlagMinPolePct`, `FlagMinPoleRatio`) and the tight-flag gate (`RequireTightFlag`, `FlagLookback`, `FlagMaxRangePct`, `FlagContractionRatio`), direction filter (both/long/short), the re-arm/anti-spam mode, and warm-boot depth.

Adjust these before running `task build`. Treat the shipped defaults as starting points and backtest before relying on the scores or verdicts.
