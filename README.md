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

It follows the CryptoLifer / Sam Price "model" and detects it as an **ordered sequence** on closed candles — each phase has to happen after the previous one, or nothing fires:

**21/200 cross → flagpole → flag → first touch of the 21 SMA**

A pullback all the way to the **200 SMA** invalidates the setup.

**How it works:**

1. **Cross (regime).** A golden cross (21 SMA above 200 SMA) starts the search for a **long** setup; a death cross for a **short** one. Both directions are watched. There is no alert on the cross itself. A trend whose cross the module has not seen (e.g. older than the warm-boot history) is not traded.
2. **Flagpole.** After the cross, price must launch an impulsive leg in the trend direction: from its origin (the lowest low in the window, for a long) to a new extreme ≥ `MinPoleMovePct`, covered within `MaxPoleBars` bars — a slow drift never gets there in time. At the extreme, price must also stand ≥ `MinPoleExtPct` away from the 21 SMA: that gap is what the flag later closes. Price riding on the 21 never makes a pole, so it never alerts.
3. **Flag.** After the pole's extreme, price pulls back while the 21 SMA catches up. A new extreme extends the pole and restarts the flag. The flag breaks — and the pole is discarded — if it gives back more than `MaxFlagRetrace` of the pole, closes through the 21 SMA, or hasn't reached the 21 within `MaxFlagBars` bars.
4. **Touch (entry).** The first bar that reaches the 21 SMA (within a small tolerance band) **and closes back on the trend side** completes the flag. It alerts only if the flag is real and the trend is still intact:
   - the flag lasted ≥ `MinFlagBars` bars (a quicker touch is a snap back, not a flag);
   - the **21 SMA caught up to price** — it closed ≥ `MinSMACatchUp` of the gap that stood between it and the pole's extreme. This is what separates a flag (price goes sideways, the 21 comes to it) from a V (price falls straight back onto the 21);
   - the pullback ran no faster than `MaxFlagSpeedRatio` × the pole's speed;
   - the **21 SMA is still curving in the trend direction** — up for a long, down for a short — by ≥ `MinSlopePct` over `SlopeBars` bars.

   Either way that first touch consumes the pole: another alert needs a fresh pole and flag.
5. **Invalidation.** If a pullback reaches the 200 SMA, the setup is disarmed until the next cross. (An optional note can be emitted when this happens.)
6. **Anti-spam.** By default only the **first setup after each cross** alerts (`MaxSetupsPerCross=1`; `0` allows every fresh pole + flag in the trend). On top of that, a **per-direction cooldown** caps the rate at one alert per direction per `CooldownMin` minutes (15 by default). LONG and SHORT have independent timers, so a regime flip can alert immediately. This cooldown applies to the SMA retest alert only — no other notification is affected.
7. **Warm boot.** On startup the module fetches ~1000 closed 1m candles from REST and **replays them silently** through the same state machine. The regime, a flag still forming, and setups that already alerted are rebuilt exactly, so a restart neither misses a forming flag nor re-alerts an old one.

The journal (`[SMARETEST]` lines) records every flagpole the module finds and why each flag was dropped (snap back, V onto the 21, flat 21, closed through, stale, cooldown), so "it touched the 21 — why no alert?" can be answered from the logs.

On ~20 days of Bybit BTCUSDT 1m candles, the shipped defaults fire roughly once or twice a day, on setups that visually match the model; the previous gates fired ~6×/day, mostly on chop and V-bounces.

**Outcome logging (measurement).** None of the rules above are a validated edge — so for every retest it fires, the module logs a `[SMARETEST-OUTCOME-T0]` line (entry price, pole size and speed, flag length, retrace, how much of the gap the 21 closed, 21 SMA slope, bars since cross) and, at each configured horizon (15/30/60 min by default), a `[SMARETEST-OUTCOME-FWD]` line with the realised forward return and whether it moved in the trade's favour. The forward prices come from the module's own live candle stream (no extra REST calls), and both lines share an `id=<entry time>` join key. Grep the logs to compute the signal's real hit-rate from data instead of impression, and re-tune the thresholds from there.

**Data source.** The 1m candles come from the primary exchange (Bybit perp BTC/USDT by default) over a WebSocket kline subscription, with a REST poll as an automatic fallback if the socket goes quiet — so a dropped connection or a geo-blocked REST host (it transparently fails over to Bybit's `bytick.com` mirror) does not silence the feed.

> ⚠️ **1m is fast and noisy, and this is plumbing for a setup signal — not a validated edge.** Expect many alerts, more whipsaw, and transaction costs that bite a larger fraction of each move than on higher timeframes; runaway trends that never retest the 21 SMA are missed by design. The shipped 1m thresholds are volatility-scaled starting points — use the outcome logs to validate and re-tune the long and short legs separately before acting on them. Every threshold lives in the module's config block.

## 🧹 Liquidity Sweep Alerts (Independent Module)

A third, fully self-contained module (`internal/sweep`) watches **closed 5-minute BTCUSDT candles** for a textbook **liquidity sweep**: price runs the stops beyond an obvious level, then snaps back. It has its own Bybit and OKX streams, state and messages, and shares nothing with the other alerts.

**What has to happen — all of it, on one candle:**

| Rule | Default | Why |
|---|---|---|
| **A liquidity level is taken out** — a 1h swing high/low (2 hours either side didn't go beyond it), the previous UTC day's high/low, or the previous week's. Equal highs/lows (within `EqualLevelTolPct`) merge into one level. | levels up to 7 days old | That's where stops and liquidation prices rest. |
| The wick clears the level by **enough, but not too much** | 0.05 – 1.5 × ATR | A one-tick poke isn't a stop run; running far beyond is a breakdown that happened to bounce. |
| The candle **closes back inside** the level | — | Closing beyond it is a break, not a sweep. |
| A **big rejection wick** | ≥ 50% of the candle and ≥ 1 × ATR(14) | The rejection has to be visible, not a body-heavy candle. |
| **Heavy volume** | ≥ 2.5 × the median of the last 4h | Stops being hit create a volume burst. |
| **Liquidations on the swept side** — longs for a swept low, shorts for a swept high — Bybit + OKX, inside that candle | ≥ $250k and ≥ 70% one-sided | Proof that leveraged positions were actually flushed. The Bybit feed must have been connected for the whole candle. |
| **Cooldown** | 1 alert per direction per 60 min | |

Each level can alert only once: any candle that trades through it spends it, sweep or not. On startup the module replays ~8 days of candles silently to rebuild the levels (history has no liquidation data, so it can never alert on the past), and it backfills candles missed during a reconnect the same way.

**Be clear about what it is.** Before building it, the rules were backtested on 90 days of BTC data (Bybit candles, Bybit open interest, and Binance BTCUSDT futures taker buy/sell volume), tuned on 60 days and checked on the last 30. **No candle-based version beat chance**: textbook sweeps of swing, 1h, daily and weekly levels, with any wick size, volume spike, open-interest drop, taker-flow absorption or structure-shift confirmation, reached +2R before the wick was taken out about 30–35% of the time — the same as a random candle with the same stop. Historical liquidation data isn't available anywhere for free, so the liquidation rule — the one ingredient that makes a sweep a sweep — could not be tested. That's why the alert says *"a sweep, not a guaranteed reversal"*, and why every alert is measured:

**Outcome logging.** Each alert logs `[SWEEP-OUTCOME-T0]` (entry, invalidation, risk and every feature), `[SWEEP-OUTCOME-STOP]` if price later trades beyond the wick (the sweep failed), and `[SWEEP-OUTCOME-FWD]` at 15/30/60/240 minutes (return from entry, best R reached, stopped or not). After a few weeks, grep these to see whether liquidation-confirmed sweeps actually beat 35% at 2R — then keep, tighten or remove the module. Every candle that took out a level but failed a rule is logged too (`[SWEEP] … rejected: <reason>`).

**Tweak or remove it.** Every rule is a field in `sweep.Config` (`internal/sweep/config.go`). `LogOnly: true` keeps it running and logging without sending anything; `RequireLiquidations: false` alerts on the candle rules alone. To remove it completely, delete the single `sweep.Run(...)` call in `main.go` and the `internal/sweep` folder.

```markdown
🧹 LIQUIDITY SWEEP 🟢 BULLISH

🎯 Swept previous day low $63,520
      + 1h swing low $63,480 (tested 2×, formed 9h ago)
💲 BTC $63,704 · 5m candle closed back above

🕯 Wick to $63,410 · 2.1× ATR · 68% of the candle
📊 Volume 3.4× the 4h median
💥 $1.2M longs liquidated (Bybit $934k · OKX $300k)

🛑 Invalidated below $63,410
ℹ️ Stops were run and the level reclaimed — a sweep, not a guaranteed reversal.
```

> Bearish sweeps mirror this (🔴, swept highs, shorts liquidated, invalidated above). Level names, prices and the liquidation split are bold in Telegram. The values above are illustrative.

## ✨ Key Features

- **Zero Alert Fatigue:** 5-minute rolling windows and configurable volume confluence filters ensure you only get notified during major volatility blocks.
- **Directional Context Label:** Each alert is labelled from combined Open Interest flow — *reversal up* (capitulation), *continuation*, or *unclear* — as plain context on the raw event, not a scored buy/sell signal.
- **21/200 SMA Retest Module:** A fully independent add-on that watches 1-minute candles for bar-close retests of the 21 SMA after a 21/200 cross (both long and short), with a 200-SMA invalidation guard, anti-spam debounce, per-signal forward-return outcome logging, and a WebSocket-primary / REST-fallback candle feed.
- **Liquidity Sweep Module:** A fully independent add-on that alerts on strict 5-minute stop runs through 1h swing, previous-day and previous-week levels — big rejection wick, heavy volume and live Bybit + OKX liquidations on the swept side — with per-alert outcome logging and a log-only switch.
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
Touch low: 63689.20
Room to 200 SMA: 0.82%
Flagpole: 0.95% in 8 bars (0.41% above the 21)
Flag: 9 bars, gave back 38% of the pole; the 21 closed 52% of the gap
21 SMA rising: 0.085% over 5 bars
Regime: 34 bars since golden cross
Cross + flagpole + flag + kiss of the rising 21 SMA (support held) — model entry.
```

> Short setups mirror this (death cross, `Touch high`, a falling 21, "resistance held"). The values above are illustrative.

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
- **SMA Retest module** (`internal/smaretest` config block): timeframe (`1m` by default) and SMA periods, the 21-SMA touch tolerance (percent band or ATR-based), the flagpole (`MinPoleMovePct`, `MaxPoleBars`, `MinPoleExtPct`), the flag (`MinFlagBars`, `MaxFlagBars`, `MaxFlagRetrace`, `MaxFlagSpeedRatio`, `MinSMACatchUp`), the 21 SMA slope (`SlopeBars`, `MinSlopePct`), direction filter (both/long/short), setups per cross (`MaxSetupsPerCross`; 1 by default, `0` unlimited) and the per-direction alert cooldown (`CooldownMin`; 15 minutes, `0` disables), the forward-return horizons for outcome logging (`OutcomeHorizonsMin`), and warm-boot depth.
- **Liquidity Sweep module** (`internal/sweep` config block): level sources and merging (`SwingStrength`, `EqualLevelTolPct`, `MaxLevelAgeHours`), the sweep candle (`MinPenetrationATR`, `MaxPenetrationATR`, `MinWickRatio`, `MinWickATR`, `MinVolumeRatio`, `VolumeLookback`), the liquidation confirmation (`RequireLiquidations`, `MinLiqUSD`, `MinLiqSideShare`, `LiqGraceSec`), `CooldownMin`, `LogOnly`, and the outcome horizons (`OutcomeHorizonsMin`).

Adjust these before running `task build`. Treat the shipped defaults as starting points and validate against real events before trading on them.
