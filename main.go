package main

import (
	"log"
	"net/http"
	"os"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
	"github.com/robmeijerink/MarktPandaBot/internal/bybit"
	"github.com/robmeijerink/MarktPandaBot/internal/okx"
	"github.com/robmeijerink/MarktPandaBot/internal/smaretest"
	"github.com/robmeijerink/MarktPandaBot/internal/sweep"
	"github.com/robmeijerink/MarktPandaBot/internal/telegram"
)

const (
	HealthCheckPort = ":8080"
)

func main() {
	// Log everything to stdout (not a file) so it is captured by the
	// container/service runtime (Docker, GCE) and can be reviewed live.
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.LUTC)

	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if telegramToken == "" || chatID == "" {
		log.Fatal("[MAIN] Environment variables TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	aggr := &aggregator.Aggregator{}
	state := &aggregator.MarketState{}

	log.Println("[MAIN] Starting data streams and decision engine...")

	// Primary Streams (Liquidations)
	go okx.MaintainOKXLiquidations(aggr)
	go bybit.MaintainBybitLiquidations(aggr)

	// Secondary Streams (Stateful Context: Funding, OI & Price)
	go okx.MaintainOKXContext(state)
	go bybit.MaintainBybitTickers(state)

	// Decision Engine — raw liquidation confluence alert (a volatility radar: it
	// reports significant multi-venue flushes with OI/funding context; it does not
	// score or grade them).
	go aggregator.RunConfluenceEngine(aggr, state, telegramToken, chatID)

	// 21/200 SMA retest module (upgrade.md) — fully independent add-on. This is the
	// single permitted wiring call: it warm-boots its own 1m regime and streams its
	// own 1m klines (WS primary, REST fallback), sending its own Telegram messages.
	// It shares no state with the engine above.
	go smaretest.Run(smaretest.DefaultConfig(), func(msg string) {
		telegram.DispatchTelegramAlert(telegramToken, chatID, msg)
	})

	// Liquidity sweep module (internal/sweep) — fully independent add-on with its own
	// Bybit/OKX streams and messages. Remove it by deleting this call; mute it without
	// removing by setting LogOnly in its Config; tune every rule in that same Config.
	go sweep.Run(sweep.DefaultConfig(), func(msg string) {
		telegram.DispatchTelegramAlert(telegramToken, chatID, msg)
	})

	// Health Check for Docker/Google Cloud Engine
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Printf("Starting application. Health check listening on %s", HealthCheckPort)
	if err := http.ListenAndServe(HealthCheckPort, nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
