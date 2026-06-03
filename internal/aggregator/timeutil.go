package aggregator

import "time"

// All candle/time math is in UTC (D6). 5-minute boundaries are xx:00, xx:05, …
// UTC. The host clock MUST be NTP-synced for the candle-sync confirmation to land
// on the right closed candle.

// floorTo5Min returns the start of the UTC 5-minute bucket containing t.
// time.Truncate operates on the absolute instant, and 5 minutes divides the hour
// evenly with the Unix epoch aligned to a boundary, so this yields xx:00, xx:05…
func floorTo5Min(t time.Time) time.Time {
	return t.UTC().Truncate(5 * time.Minute)
}

// ceilToNext5Min returns the first UTC 5-minute boundary strictly after t. When t
// is exactly on a boundary it returns the next one (never t itself), matching the
// "strictly after now" requirement of §6.
func ceilToNext5Min(t time.Time) time.Time {
	return floorTo5Min(t).Add(5 * time.Minute)
}

// confirmationTarget implements the §6 target-time rule. It returns the UTC time
// of a fully-closed candle to evaluate the confirmation against:
//
//	nextClose = ceilToNext5Min(now)            // strictly after now
//	if (nextClose - now) < MinLeadSeconds:
//	    target = nextClose + CandleIntervalSec
//	else:
//	    target = nextClose
//
// This guarantees an effective wait of [MinLeadSeconds, MinLeadSeconds+CandleInterval)
// — ~3–8 minutes with the defaults — so the evaluated candle began after the
// flush settled.
func confirmationTarget(now time.Time, cfg Config) time.Time {
	now = now.UTC()
	nextClose := ceilToNext5Min(now)
	if nextClose.Sub(now) < time.Duration(cfg.MinLeadSeconds)*time.Second {
		return nextClose.Add(time.Duration(cfg.CandleIntervalSec) * time.Second)
	}
	return nextClose
}
