// Package calendar sends a daily morning briefing of the macro-economic events that
// tend to move the Bitcoin price (US inflation, jobs, Fed decisions, …). It reads the
// public Forex Factory weekly calendar feed once a day, scores every event 1–5 for how
// much BTC volatility it usually brings, and posts the relevant ones in local time.
//
// The score describes expected volatility only — never a direction or a trade.
//
// REMOVAL / TWEAKS: the package owns its own fetch, schedule and message and shares
// nothing with the other alerts. It is wired in with one call in main.go — delete that
// call (and this folder) to remove it. Every setting lives in Config.
package calendar

import (
	"time"
	_ "time/tzdata" // embed the zone database: the static binary may run without /usr/share/zoneinfo
)

// Config is the single source of truth for the module; the comments give the defaults.
type Config struct {
	Timezone   string // "Europe/Amsterdam" — schedule and event times use this zone
	SendHour   int    // 7
	SendMinute int    // 0

	WindowHours int // 24 — the briefing covers events from send time until this many hours later
	MinScore    int // 2 — events scoring below this are left out; a day with none sends nothing

	FeedURL         string // Forex Factory weekly JSON feed
	FetchTimeoutSec int    // 15
	FetchRetries    int    // 3 — the feed rate-limits aggressively, so retries are spaced out…
	RetryDelaySec   int    // 120 — …by this much
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Timezone:        "Europe/Amsterdam",
		SendHour:        7,
		SendMinute:      0,
		WindowHours:     24,
		MinScore:        2,
		FeedURL:         "https://nfs.faireconomy.media/ff_calendar_thisweek.json",
		FetchTimeoutSec: 15,
		FetchRetries:    3,
		RetryDelaySec:   120,
	}
}

// nextSend returns the first send time strictly after now, in loc. time.Date
// normalises across DST changes, so 07:00 stays 07:00 local all year.
func nextSend(cfg Config, now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	t := time.Date(local.Year(), local.Month(), local.Day(), cfg.SendHour, cfg.SendMinute, 0, 0, loc)
	if !t.After(local) {
		t = time.Date(local.Year(), local.Month(), local.Day()+1, cfg.SendHour, cfg.SendMinute, 0, 0, loc)
	}
	return t
}
