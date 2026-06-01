package main

import (
	"log"
	"net/http"
	"os"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
	"github.com/robmeijerink/MarktPandaBot/internal/binance"
	"github.com/robmeijerink/MarktPandaBot/internal/bybit"
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
	go binance.MaintainBinanceForceOrders(aggr)
	go bybit.MaintainBybitLiquidations(aggr)

	// Secondary Streams (Stateful Context: Funding & OI)
	go binance.MaintainBinanceMarkPrice(state)
	go bybit.MaintainBybitTickers(state)

	// Decision Engine
	go aggregator.RunConfluenceEngine(aggr, state, telegramToken, chatID)

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
