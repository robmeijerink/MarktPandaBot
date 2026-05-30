# 🐼 MarktPandaBot: Liquidation & Context Tracker

A stateful BTCUSDT Telegram alert system designed to detect genuine Support/Resistance (S/R) breakouts by analyzing cryptocurrency liquidation clusters, Open Interest (OI), and Funding Rates.

Most liquidation bots spam your feed with every single forced order, leading to severe alert fatigue. This tracker solves that by acting as a high-pass filter: it aggregates live market data across multiple WebSockets and only notifies you when a significant, multi-exchange market shift occurs, complete with the underlying market context.

## 📡 Live Telegram Channel

Don't want to deal with Go environments, CI/CD pipelines, or API limits? You can use the MarktPanda Bot live for FREE.

Join the public Telegram channel to instantly receive:
- Real-time combined Binance & Bybit liquidation alerts
- Clear visual breakdowns of Long vs. Short liquidations
- Market funding rates & Open Interest metrics

🔗 **[Join the official MarktPanda Channel](https://t.me/marktpanda)**


## 🎯 What it is

The Liquidation Confluence Tracker is an automated, concurrent market monitor written in Go. It watches for massive liquidation events in the crypto futures market (specifically BTC/USDT). Instead of forwarding raw data, it groups liquidations into 5-minute time windows and evaluates them against strict volume thresholds.

If the liquidations indicate a true market exhaustion or a massive breakout, it pushes a highly condensed, easily scannable alert directly to your Telegram or Smartwatch, enriched with real-time Open Interest and Funding Rate data to validate the market's true direction.

## ⚙️ How it Works

The core of this tracker is built on a **Stateful Confluence Strategy** (Global Truth + Local Confirmation + Context). It simultaneously maintains concurrent WebSocket connections to two major exchanges:

1. **Global Truth (Binance):** Binance represents the macro-market. The tracker watches the `forceOrder` and `markPrice` streams here. If massive liquidation clusters occur on Binance (e.g., > 5 BTC within 5 minutes), the whole market moves.

2. **Local Confirmation & Context (Bybit):** Bybit provides the secondary confirmation via the `allLiquidation` and `tickers` streams. A global move is only actionable if validated by Bybit volume (e.g., > 100,000 USDT). Furthermore, Bybit provides the crucial real-time **Open Interest** data to see if new money is entering or leaving the market during the liquidation cascade.

### The Alert Lifecycle

1. **Listen:** Goroutines silently collect real-time forced orders, while parallel workers continuously update a shared `MarketState` (protected by a Read-Write Mutex) with the latest Open Interest and Funding Rates.
2. **Aggregate:** Every 5 minutes, the engine calculates the total liquidated volume, order count, and the exact price range (slippage) of those liquidations.
3. **Evaluate:** It checks if the aggregated volume crosses the predetermined thresholds for *both* exchanges simultaneously.
4. **Notify:** If confluence is achieved, the bot safely reads the latest OI and Funding contexts, formats a minimalist alert, and dispatches it via the Telegram API.

## ✨ Key Features

- **Zero Alert Fatigue:** 5-minute rolling windows and strict volume thresholds ensure you only get notified during major volatility.
- **Stateful Context Engine:** Doesn't just report the crash; it reports the context. Real-time Open Interest and Funding Rates are attached to every alert to help identify Short Squeezes and trap setups.
- **Smartwatch Optimized:** Alerts are meticulously formatted using minimal text and clean line breaks, allowing you to read Volume, Range, Funding, and OI at a single glance on your wrist.
- **DevOps Ready:** Compiled as a 100% statically linked Alpine Linux binary (`CGO_ENABLED=0`). Extremely lightweight footprint (~30MB RAM), perfect for hosting on a GCP `e2-micro` instance.

## 📱 Alert Format Example

```markdown
🚨 **LIQUIDATION ALERT**
_⚠️ Combined (Binance & Bybit): ~509800 $ liquidated in the past 5 minutes._

🌐 **BINANCE** (Total: 5.20 ₿ / ~384800 $)
🔴 Longs: 4.00 ₿ (~296000 $)
🟢 Shorts: 1.20 ₿ (~88800 $)
Ord: 18
Rng: 74000 - 74150
Fund: 0.0100%

📍 **BYBIT** (Total: 125000 $)
🔴 Longs: 100000 🟢 Shorts: 25000
Ord: 22
Rng: 74010 - 74145
Fund: 0.0100%
OI: 85000000
```

## 🚀 Setup & Configuration

### Prerequisites

- Docker (Colima/Orbstack for macOS)

- Taskfile (`go-task`)


### Environment Variables

To run this tracker, you must provide the following environment variables:

- `TELEGRAM_TOKEN`: The API token provided by the BotFather.

- `TELEGRAM_CHAT_ID`: The ID of your public or private channel (always starts with `-100`).


### Local Testing

Run the bot interactively using the Alpine Go container:

```bash
task run
```

### Production Build

Compile the static Linux binary for deployment:


```
task build
```

This generates the `marktpanda_bot` executable, which can be deployed directly to your VPS via SCP and run as a Systemd service. Thresholds (like the 5 BTC global limit) can be adjusted in the `const` block of `main.go` prior to building.
