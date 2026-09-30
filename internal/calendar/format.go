package calendar

import (
	"fmt"
	"strings"
	"time"
)

type scoredEvent struct {
	Event
	score int
}

var scoreDots = map[int]string{5: "🔴", 4: "🟠", 3: "🟡", 2: "⚪", 1: "⚪"}

var countryFlags = map[string]string{
	"USD": "🇺🇸", "EUR": "🇪🇺", "GBP": "🇬🇧", "JPY": "🇯🇵", "CNY": "🇨🇳",
	"CAD": "🇨🇦", "AUD": "🇦🇺", "NZD": "🇳🇿", "CHF": "🇨🇭",
}

// selectEvents keeps the events in [from, from+window) that score at least minScore.
// A holiday is a whole-day entry, so one dated earlier on the same local day still counts.
func selectEvents(cfg Config, events []Event, from time.Time, loc *time.Location) []scoredEvent {
	to := from.Add(time.Duration(cfg.WindowHours) * time.Hour)
	fromLocal := from.In(loc)
	var out []scoredEvent
	for _, e := range events {
		inWindow := !e.Time.Before(from) && e.Time.Before(to)
		if strings.EqualFold(e.Impact, "holiday") && sameDay(e.Time.In(loc), fromLocal) {
			inWindow = true
		}
		if !inWindow {
			continue
		}
		if s := score(e); s >= cfg.MinScore {
			out = append(out, scoredEvent{Event: e, score: s})
		}
	}
	return out
}

// buildBriefing renders the morning message (legacy Markdown: *bold* only, so every
// feed value is escaped). It opens with the 🗓 prefix so it stands apart from the
// other alerts in the feed. It returns "" when there is nothing to send.
func buildBriefing(cfg Config, events []scoredEvent, now time.Time, loc *time.Location) string {
	local := now.In(loc)
	if len(events) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🗓 *MACRO CALENDAR* — %s\n", local.Format("Mon 2 Jan"))
	fmt.Fprintf(&sb, "Next %dh · times in %s\n\n", cfg.WindowHours, local.Format("MST"))

	top := events[0]
	for _, e := range events[1:] {
		if e.score > top.score {
			top = e
		}
	}
	fmt.Fprintf(&sb, "⚡ Biggest: *%d/5* at %s — %s\n\n", top.score, eventTime(top, local, loc), escape(top.Title))
	// Two lines per event, a blank line between: time and name, then score and numbers.
	for _, e := range events {
		flag := countryFlags[e.Country]
		if flag == "" {
			flag = escape(e.Country)
		}
		fmt.Fprintf(&sb, "*%s* %s %s\n", eventTime(e, local, loc), flag, escape(e.Title))
		scoreLine := fmt.Sprintf("%s %d/5", scoreDots[e.score], e.score)
		if details := eventDetails(e.Event); details != "" {
			scoreLine += " · " + details
		}
		sb.WriteString(scoreLine + "\n\n")
	}
	sb.WriteString("ℹ️ Score 1–5 = expected BTC volatility.")
	return sb.String()
}

// eventTime is HH:MM, prefixed with the weekday when the event is not today; holidays read "All day".
func eventTime(e scoredEvent, today time.Time, loc *time.Location) string {
	t := e.Time.In(loc)
	if strings.EqualFold(e.Impact, "holiday") {
		return "All day"
	}
	if !sameDay(t, today) {
		return t.Format("Mon 15:04")
	}
	return t.Format("15:04")
}

func eventDetails(e Event) string {
	if strings.EqualFold(e.Impact, "holiday") {
		return "Bank holiday — thin liquidity"
	}
	var parts []string
	if e.Forecast != "" {
		parts = append(parts, "fcst "+escape(e.Forecast))
	}
	if e.Previous != "" {
		parts = append(parts, "prev "+escape(e.Previous))
	}
	return strings.Join(parts, " · ")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

var markdownEscaper = strings.NewReplacer("_", `\_`, "*", `\*`, "`", "\\`", "[", `\[`)

func escape(s string) string { return markdownEscaper.Replace(s) }
