package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
	"github.com/robmeijerink/MarktPandaBot/internal/bybit"
	"github.com/robmeijerink/MarktPandaBot/internal/okx"
	"github.com/robmeijerink/MarktPandaBot/internal/smaretest"
	"github.com/robmeijerink/MarktPandaBot/internal/telegram"
	"github.com/robmeijerink/MarktPandaBot/internal/volume"
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

	// Two-stage scoring upgrade (upgrade.md): config, the aggregated 5-min volume
	// ring, the perp/spot flow tracker, and the cancelable confirmation manager.
	cfg := aggregator.DefaultConfig()
	ring := aggregator.NewVolumeRing(cfg)
	flow := aggregator.NewFlowTracker()

	klineClient := &http.Client{Timeout: time.Duration(cfg.KlineFetchTimeoutSec) * time.Second}

	// Reclaim reference: closing price of the target candle on PrimaryExchange
	// (Bybit). If Bybit REST is geo-blocked (HTTP 403) we fall back to the OKX
	// close so the confirmation still yields a real verdict instead of always
	// reading "Not confirmed". BTC basis between the two venues is a few dollars,
	// negligible against a liquidation-range reclaim level. Shared by the
	// confirmation stage and the outcome logger's forward-return reads.
	fetchClose := func(bucketStart time.Time) (float64, bool) {
		if px, ok := bybit.FetchClose(klineClient, bucketStart); ok {
			return px, true
		}
		return okx.FetchClose(klineClient, bucketStart)
	}
	confMgr := aggregator.NewConfirmationManager(cfg, flow, fetchClose,
		func(msg string) { telegram.DispatchTelegramAlert(telegramToken, chatID, msg) },
	)

	// Outcome logger: labels each alert and records its forward return so the
	// real-world accuracy of each T0 signal can be measured (see [OUTCOME-*] logs).
	outcomeLogger := aggregator.NewOutcomeLogger(fetchClose, cfg.OutcomeHorizonsMin)

	// Warm boot hydrates the volume ring from REST BEFORE any WebSocket opens
	// (§3). It is best-effort and never blocks startup indefinitely.
	log.Println("[MAIN] Warm-booting volume ring from REST klines...")
	volume.WarmBoot(ring, cfg)

	log.Println("[MAIN] Starting data streams and decision engine...")

	// Primary Streams (Liquidations)
	go okx.MaintainOKXLiquidations(aggr)
	go bybit.MaintainBybitLiquidations(aggr)

	// Secondary Streams (Stateful Context: Funding, OI & Price)
	go okx.MaintainOKXContext(state)
	go bybit.MaintainBybitTickers(state)

	// Trade Streams (perp CVD + spot, for the T+N confirmation, §6)
	go bybit.MaintainBybitPerpTrades(flow)
	go bybit.MaintainBybitSpotTrades(flow)
	go okx.MaintainOKXTrades(flow)

	// Live volume poll: append one aggregated bucket per UTC 5-min boundary.
	go volume.RunLivePoll(ring, cfg)

	// Decision Engine
	go aggregator.RunConfluenceEngine(aggr, state, cfg, ring, confMgr, flow, outcomeLogger, telegramToken, chatID)

	// 21/200 SMA retest module (upgrade.md) — fully independent add-on. This is the
	// single permitted wiring call: it warm-boots its own 3m regime and streams its
	// own 3m klines (WS primary, REST fallback), sending its own Telegram messages.
	// It shares no state with the engine above.
	go smaretest.Run(smaretest.DefaultConfig(), func(msg string) {
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
