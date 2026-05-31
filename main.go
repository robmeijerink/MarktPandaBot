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
	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if telegramToken == "" || chatID == "" {
		log.Fatal("Environment variables TELEGRAM_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	aggr := &aggregator.Aggregator{}
	state := &aggregator.MarketState{}

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
