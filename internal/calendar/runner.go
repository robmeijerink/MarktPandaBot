package calendar

import (
	"log"
	"net/http"
	"time"
)

// Run posts the briefing every day at cfg.SendHour:cfg.SendMinute in cfg.Timezone.
// It blocks forever; start it in its own goroutine. A restart after the send time
// waits for the next day, so it never double-posts. A day without relevant events
// sends nothing.
func Run(cfg Config, send func(string)) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Printf("[CALENDAR] Unknown timezone %q (%v); module disabled.", cfg.Timezone, err)
		return
	}
	client := &http.Client{Timeout: time.Duration(cfg.FetchTimeoutSec) * time.Second}
	for {
		at := nextSend(cfg, time.Now(), loc)
		log.Printf("[CALENDAR] Next briefing at %s.", at.Format("Mon 2 Jan 15:04 MST"))
		time.Sleep(time.Until(at))

		events, err := fetchWithRetry(client, cfg)
		if err != nil {
			log.Printf("[CALENDAR] Feed unavailable, skipping today's briefing: %v", err)
			continue
		}
		now := time.Now()
		selected := selectEvents(cfg, events, now, loc)
		if msg := buildBriefing(cfg, selected, now, loc); msg != "" {
			log.Printf("[CALENDAR] Sending briefing with %d events.", len(selected))
			send(msg)
		} else {
			log.Printf("[CALENDAR] No relevant events in the next %dh; nothing sent.", cfg.WindowHours)
		}
	}
}

func fetchWithRetry(client *http.Client, cfg Config) ([]Event, error) {
	var lastErr error
	for attempt := 0; attempt <= cfg.FetchRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(cfg.RetryDelaySec) * time.Second)
		}
		events, err := fetchEvents(client, cfg.FeedURL)
		if err == nil {
			return events, nil
		}
		lastErr = err
		log.Printf("[CALENDAR] Feed fetch attempt %d failed: %v", attempt+1, err)
	}
	return nil, lastErr
}
