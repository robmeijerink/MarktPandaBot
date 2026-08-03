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

The alert is deliberately a **volatility radar**, not a trade signal: it fires when a significant, multi-venue flush happens and hands you the context (OI flow, funding, per-venue tables) — the decision of whether and how to trade is yours (for example, using the independent 1-minute SMA-retest module below).

## ⚙️ How it Works

The core of this tracker is built on a **Stateful Confluence Strategy** (Global Truth + Local Confirmation + Context). It simultaneously maintains concurrent WebSocket connections to two major derivatives exchanges:

1. **Exchange One (OKX):** The tracker hooks into the public liquidation and ticker streams. If massive liquidation clusters occur on OKX (crossing the configured evaluation thresholds within 5 minutes), it signals major algorithmic execution.

2. **Exchange Two (Bybit):** Bybit provides the secondary confirmation via its `allLiquidation` and `tickers` streams. A market move is only actionable if validated by Bybit volume concurrently. Furthermore, both exchanges provide crucial real-time **Open Interest** and delta changes to see if new money is aggressively entering or leaving the market during the liquidation cascade.

### The Alert Lifecycle

1. **Listen:** Goroutines silently collect real-time forced orders, while parallel workers continuously update a shared `MarketState` (protected by a Read-Write Mutex) with the latest Open Interest and Funding Rates from both OKX and Bybit.
2. **Aggregate:** Every 5 minutes, the engine calculates the total liquidated volume (normalized to USDT/USD), order count, the biggest single liquidation print, and the exact price range (slippage) of those liquidations.
3. **Evaluate:** It checks if the aggregated volume crosses the configured confluence thresholds for *both* exchanges simultaneously.
4. **Notify:** If confluence is achieved, the bot safely reads the latest OI and Funding contexts, formats a minimalist, smartwatch-optimized alert, and dispatches it via the Telegram API.

The alert also carries a **directional label** derived from combined Open Interest flow: OI falling while longs are flushed reads as a potential *reversal up* (capitulation); OI rising reads as a *continuation*; a small OI move is left *unclear*. This is a plain label on the raw event — there is no scoring, grading, or follow-up confirmation. Treat it as context, not a buy/sell instruction.

> ⚠️ **This is a volatility radar, not a validated edge.** A violent flush is an *event*, not a setup — in a strong trend it is often just a pause before continuation. Use the alert to bring your attention to the chart, then apply your own entry rules (e.g. the 1-minute SMA-retest model below).

## 📐 21/200 SMA Retest Alerts (Independent Module)

A separate, fully self-contained module watches for **pullback / retest entries** on the **1-minute timeframe** (the timeframe the CryptoLifer / Sam Price model is actually taught on). It is completely decoupled from the liquidation engine above — it keeps its own state, streams its own candles, and sends its own messages. It shares no data with the liquidation engine and cannot affect the base liquidation alerts.

The idea is mechanical, not pattern-matching, and follows the CryptoLifer / Sam Price "model": a 21/200 SMA cross sets the trend, a sudden **flagpole** overextends price away from the 21 SMA, and the module then takes the **pullback that kisses the 21 SMA** (dynamic support for longs, resistance for shorts) as the entry confirmation. A pullback all the way to the **200 SMA** invalidates the setup.

**How it works:**

1. **Trend (regime).** A golden cross (21 SMA above 200 SMA) arms **long** retest setups; a death cross arms **short** setups. Both directions are watched. There is no alert on the cross itself — only on the subsequent confirmed entry.
2. **Flagpole gate.** A kiss only counts as an entry when a **recent, aggressive flagpole** preceded it: price must have overextended ≥ `MinSeparationPct` away from the 21 SMA (in the trend direction) with that peak falling inside the last `PoleWindow` bars. The recency window is what encodes "aggressive" — covering that gap within a short window is an impulse, whereas a slow drift never reaches the depth in time. The kiss bar itself is excluded, so the pole is always a *prior* move and the touch proves the return. Without this gate, price sitting on the 21 SMA would fire on every local mean reversion. Disable with `RequirePole=false`.
3. **Bar-close kiss.** A long entry fires only when the candle's low reaches the 21 SMA (within a small tolerance band) **and the candle closes back at or above it** — a wick that pierces the SMA but closes below is rejected (the setup is invalidated). Shorts mirror this. This close-on-the-right-side filter is the whole point of evaluating on closed bars.
4. **Flag gate.** After the pole, the alert waits for a real **flag** to form before the kiss: a tight, contracting range in the bars just before the touch (recent range within `FlagMaxRangePct` of price and tighter than the earlier half by `FlagContractionRatio`, over `FlagLookback` bars). This is what stops it firing on the *first* poke at the 21 — a sharp micro-V straight back to the line, with no consolidation, is held until price settles into a flag. It's **on by default** (`RequireTightFlag=true`); turn it off to also take those immediate V-shape kisses (more, earlier alerts).
5. **Invalidation.** If a pullback reaches the 200 SMA, the setup is disarmed until the next cross. (An optional note can be emitted when this happens.)
6. **Anti-spam.** By default, when a kiss fires, the setup stays armed until price closes back outside the tolerance band (debounce mode) — allowing multiple alerts per cross, one for each clean retest of the same flagpole. This matches the model where each pullback to the 21 in a live trend is a separate entry opportunity. On top of that, a **per-direction cooldown** caps the rate at one alert per direction per `CooldownMin` minutes (15 by default): a qualifying kiss inside the window is logged but not sent, and because it is *not* consumed the setup keeps waiting and alerts on the next kiss once the window passes. LONG and SHORT have independent timers, so a regime flip can alert immediately. This cooldown applies to the SMA retest alert only — no other notification is affected.
7. **Warm boot.** On startup the module silently hydrates ~400 closed 1m candles from REST and establishes the current regime **without firing a historical alert**; the first qualifying live bar can still trigger.

**Outcome logging (measurement).** None of the gates above are a validated edge — so for every retest it fires, the module logs a `[SMARETEST-OUTCOME-T0]` line (entry price, separation, range tightness, bars since cross) and, at each configured horizon (15/30/60 min by default), a `[SMARETEST-OUTCOME-FWD]` line with the realised forward return and whether it moved in the trade's favour. The forward prices come from the module's own live candle stream (no extra REST calls), and both lines share an `id=<entry time>` join key. Grep the logs to compute the signal's real hit-rate from data instead of impression, and re-tune the thresholds from there.

**Data source.** The 1m candles come from the primary exchange (Bybit perp BTC/USDT by default) over a WebSocket kline subscription, with a REST poll as an automatic fallback if the socket goes quiet — so a dropped connection or a geo-blocked REST host (it transparently fails over to Bybit's `bytick.com` mirror) does not silence the feed.

> ⚠️ **1m is fast and noisy, and this is plumbing for a setup signal — not a validated edge.** Expect many alerts, more whipsaw, and transaction costs that bite a larger fraction of each move than on higher timeframes; runaway trends that never retest the 21 SMA are missed by design. The shipped 1m thresholds are volatility-scaled starting points — use the outcome logs to validate and re-tune the long and short legs separately before acting on them. Every threshold lives in the module's config block.

## ✨ Key Features

- **Zero Alert Fatigue:** 5-minute rolling windows and configurable volume confluence filters ensure you only get notified during major volatility blocks.
- **Directional Context Label:** Each alert is labelled from combined Open Interest flow — *reversal up* (capitulation), *continuation*, or *unclear* — as plain context on the raw event, not a scored buy/sell signal.
- **21/200 SMA Retest Module:** A fully independent add-on that watches 1-minute candles for bar-close retests of the 21 SMA after a 21/200 cross (both long and short), with a 200-SMA invalidation guard, anti-spam debounce, per-signal forward-return outcome logging, and a WebSocket-primary / REST-fallback candle feed.
- **Stateful Context Engine:** Doesn't just report the crash; it reports the context. Real-time Open Interest shifts ($\Delta$) and Funding Rates are attached to every alert to help identify Short Squeezes, long-squeezes, and trap setups.
- **Smartwatch Optimized:** Alerts are meticulously formatted using minimalist layouts, specific bold markers, and clean line breaks, allowing you to read Volume, Range, Funding, and OI delta at a single glance on your wrist.
- **DevOps Ready:** Compiled as a 100% statically linked Linux binary (`CGO_ENABLED=0`). Extremely lightweight footprint (~30MB RAM), perfect for hosting on cloud resources or micro-instances like a worker node.

## 📱 Alert Format Example

```markdown
🚨 LIQUIDATION ALERT

🔄 Likely REVERSAL UP — long capitulation

📈 OI -1.62%  ·  BTC $58,238  ·  -2.6% 24h

⚠️ Combined ~$11.7M liquidated in the last 5m

📍 BYBIT: ~$11.6M (201.14 ₿)
🔴 Long ~$11.3M
🟢 Short ~$307k
🎯 Max ~$2.6M (654 orders)
📏 Rng 57,442 - 58,526
💰 Fund +0.0031% · OI $3.57B (Δ -$52.8M)

🌐 OKX: ~$116k (2.00 ₿)
🔴 Long ~$83k
🟢 Short ~$33k
🎯 Max ~$26k (40 orders)
📏 Rng 57,543 - 58,465
💰 Fund +0.0041% · OI $2.04B (Δ -$38.1M)
```

> The values above are illustrative. Each venue is a compact block with a leading icon on every line so they read at a glance; the alert ends after the second venue — there is no score, matrix, or follow-up confirmation.
>
> **On the range (`📏 Rng`):** these are the exchange-reported **bankruptcy prices** of the liquidated positions (Bybit `p`, OKX `bkPx`), not traded OHLC. Bankruptcy price sits just beyond where the market actually traded, so a short squeeze's range prints slightly *above* the real high and a long flush's slightly *below* the real low — it can look like "a price that never printed" even though it's the correct liquidation level.

The independent SMA retest module sends its own message, with a distinct `📐` prefix so it stays readable in the same feed:

```markdown
📐 SMA RETEST — LONG (1m)
BTC/USDT  @ 63704.40
21 SMA: 63702.10   |   200 SMA: 63180.50
Touch low: 63689.20   (stop reference, not advice)
Room to 200 SMA: 0.82%
Flagpole: 0.95% overextension (within last 20 bars)
Flag: 0.31% tight range over 12 bars
Regime: 14 bars since golden cross
Flagpole + flag + kiss of the 21 SMA (support held) — model entry.
```

> Short setups mirror this (death cross, `Touch high`, "resistance held"). The "flagpole" metric shows how far price overextended from the 21 SMA within the recency window; the "flag" line shows the tight, contracting range the pullback formed before the kiss. The values above are illustrative.

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

All tunable behavior lives in one place per feature — no engine restructuring needed to adjust it:

- **Liquidation alert thresholds** — tunable constants at the top of `internal/aggregator/engine.go`: the dynamic per-venue volume bar (floor, volume-baseline fraction, volatility multiplier cap) and the OI-flow label bands (`MinOIContinuationFraction`, `MinOIReversalFraction`, `StrongOISignalFraction`) that decide reversal / continuation / unclear.
- **SMA Retest module** (`internal/smaretest` config block): timeframe (`1m` by default) and SMA periods, the 21-SMA touch tolerance (percent band or ATR-based), the flagpole gate (`RequirePole`, `MinSeparationPct`, `PoleWindow`) and the flag gate (`RequireTightFlag` on by default, `FlagLookback`, `FlagMaxRangePct`, `FlagContractionRatio`), direction filter (both/long/short), the re-arm/anti-spam mode (`ReArmMode`; default is debounce for multiple retests per cross) and the per-direction alert cooldown (`CooldownMin`; 15 minutes, `0` disables), the forward-return horizons for outcome logging (`OutcomeHorizonsMin`), and warm-boot depth.

Adjust these before running `task build`. Treat the shipped defaults as starting points and validate against real events before trading on them.
