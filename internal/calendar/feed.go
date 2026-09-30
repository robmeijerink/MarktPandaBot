package calendar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Event is one calendar entry, as published by Forex Factory.
type Event struct {
	Title    string    `json:"title"`
	Country  string    `json:"country"` // currency code: USD, EUR, … ("ALL" for global)
	Time     time.Time `json:"-"`
	Date     string    `json:"date"`   // RFC 3339 with the feed's (New York) offset
	Impact   string    `json:"impact"` // High, Medium, Low, Holiday
	Forecast string    `json:"forecast"`
	Previous string    `json:"previous"`
}

// fetchEvents downloads and parses the weekly feed, oldest first.
func fetchEvents(client *http.Client, url string) ([]Event, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MarktPandaBot/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}
	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode feed: %w", err)
	}
	out := events[:0]
	for _, e := range events {
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			continue
		}
		e.Time = t
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}
