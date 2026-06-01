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

The Liquidation Confluence Tracker is an automated, concurrent market monitor written in Go. It watches for massive liquidation events in the crypto futures market (specifically BTC/USDT). Instead of forwarding raw data, it groups liquidations into 5-minute time windows and evaluates them against strict volume thresholds.

If the liquidations indicate a true market exhaustion or a massive breakout, it pushes a highly condensed, easily scannable alert directly to your Telegram or Smartwatch, enriched with real-time Open Interest changes and Funding Rate data to validate the market's true direction.

## ⚙️ How it Works

The core of this tracker is built on a **Stateful Confluence Strategy** (Global Truth + Local Confirmation + Context). It simultaneously maintains concurrent WebSocket connections to two major derivatives exchanges:

1. **Exchange One (OKX):** The tracker hooks into the public liquidation and ticker streams. If massive liquidation clusters occur on OKX (crossing the configured evaluation thresholds within 5 minutes), it signals major algorithmic execution.

2. **Exchange Two (Bybit):** Bybit provides the secondary confirmation via its `allLiquidation` and `tickers` streams. A market move is only actionable if validated by Bybit volume concurrently. Furthermore, both exchanges provide crucial real-time **Open Interest** and delta changes to see if new money is aggressively entering or leaving the market during the liquidation cascade.

### The Alert Lifecycle

1. **Listen:** Goroutines silently collect real-time forced orders, while parallel workers continuously update a shared `MarketState` (protected by a Read-Write Mutex) with the latest Open Interest and Funding Rates from both OKX and Bybit.
2. **Aggregate:** Every 5 minutes, the engine calculates the total liquidated volume (normalized to USDT/USD), order count, the biggest single liquidation print, and the exact price range (slippage) of those liquidations.
3. **Evaluate:** It checks if the aggregated volume crosses the predetermined thresholds (e.g., 200k USDT) for *both* exchanges simultaneously.
4. **Notify:** If confluence is achieved, the bot safely reads the latest OI and Funding contexts, formats a minimalist, smartwatch-optimized alert, and dispatches it via the Telegram API.

## ✨ Key Features

- **Zero Alert Fatigue:** 5-minute rolling windows and strict volume confluence filters ensure you only get notified during major volatility blocks.
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
```

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

This generates the static `marktpanda_bot` executable, which can be deployed directly to your server instance via SCP/RSYNC and run as a standard Systemd service. Volume thresholds (such as the 200k USDT confluence limit) can be adjusted inside the configuration block of the internal package code prior to executing the build task.
