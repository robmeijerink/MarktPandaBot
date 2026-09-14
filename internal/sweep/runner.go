package sweep

import (
	"log"
	"net/http"
	"time"
)

// Run is the module entry point: it rebuilds the liquidity levels from history
// silently, then evaluates every closed candle from the Bybit stream once its late
// liquidation messages have had LiqGraceSec to arrive. It blocks forever and is meant
// to be launched as `go sweep.Run(cfg, send)`.
func Run(cfg Config, send func(string)) {
	log.Printf("[SWEEP] Starting liquidity sweep module (%s %s, liquidations required=%t, min %s, LogOnly=%t).",
		cfg.Symbol, cfg.Timeframe, cfg.RequireLiquidations, humanUSD(cfg.MinLiqUSD), cfg.LogOnly)

	liq := newLiqStore()
	d := newDetector(cfg, liq, send)
	client := &http.Client{Timeout: time.Duration(cfg.KlineFetchTimeoutSec) * time.Second}
	step := cfg.step()

	last := warmBoot(cfg, d, client)

	bars := make(chan Bar, 64)
	go runBybitFeed(cfg, bars, liq)
	go runOKXFeed(liq)

	for b := range bars {
		if !last.IsZero() && !b.Start.After(last) {
			continue // duplicate or out of order
		}
		if !last.IsZero() && b.Start.Sub(last) > step {
			backfill(cfg, d, client, last.Add(step), b.Start)
		}
		time.Sleep(time.Until(b.Start.Add(step + time.Duration(cfg.LiqGraceSec)*time.Second)))
		d.processBar(b)
		last = b.Start
	}
}

// warmBoot replays WarmBootBars of history silently so the levels, baselines and
// cooldowns are in place before the first live candle. History has no liquidation
// data, so it can never alert. It returns the last replayed candle's start (zero if
// nothing could be fetched; live candles then build the state up from scratch).
func warmBoot(cfg Config, d *detector, client *http.Client) time.Time {
	now := time.Now().UTC()
	from := now.Add(-time.Duration(cfg.WarmBootBars) * cfg.step())
	for attempt := 1; attempt <= cfg.KlineMaxRetries; attempt++ {
		bars, err := fetchBars(client, cfg, from, now, now)
		if err == nil && len(bars) > 0 {
			replay(d, bars)
			log.Printf("[SWEEP] Warm boot: replayed %d candles; %d liquidity lows and %d highs are armed.",
				len(bars), len(d.book.lows), len(d.book.highs))
			return bars[len(bars)-1].Start
		}
		log.Printf("[SWEEP] Warm boot attempt %d/%d failed (bars=%d, err=%v).", attempt, cfg.KlineMaxRetries, len(bars), err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	log.Println("[SWEEP] Warm boot failed; levels will build up from live candles.")
	return time.Time{}
}

// backfill silently replays candles the stream missed (e.g. during a reconnect) so the
// level book stays continuous. Those candles have no liquidation coverage, so they
// cannot alert anyway.
func backfill(cfg Config, d *detector, client *http.Client, from, to time.Time) {
	bars, err := fetchBars(client, cfg, from, to, time.Now().UTC())
	if err != nil {
		log.Printf("[SWEEP] Backfill %s..%s failed: %v (levels may miss those candles).", from.Format("15:04"), to.Format("15:04"), err)
		return
	}
	replay(d, bars)
	log.Printf("[SWEEP] Backfilled %d missed candles.", len(bars))
}

func replay(d *detector, bars []Bar) {
	d.silent = true
	defer func() { d.silent = false }()
	for _, b := range bars {
		d.processBar(b)
	}
}
