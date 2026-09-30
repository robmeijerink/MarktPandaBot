package smaretest

import (
	"fmt"
	"math"
)

// buildTouch renders the independent SMA-retest alert (§5). It uses a distinct
// 📐 prefix so the feed stays readable next to the existing alerts, and is sent
// as a brand-new message — it never reuses or appends to existing strings. It
// describes the pattern only — which side price tested the 21 SMA from, and the
// stats — never a trade side or an entry.
func buildTouch(cfg Config, regime int, c barCtx, s setupStats, barsSinceCross int) string {
	roomPct := math.Abs(c.bar.Close-c.slow) / c.bar.Close * 100
	from := "ABOVE"
	if regime == regimeShort {
		from = "BELOW"
	}
	return fmt.Sprintf(
		"📐 SMA RETEST — FROM %s (%s)\n"+
			"%s  @ %.2f\n"+
			"21 SMA: %.2f   |   200 SMA: %.2f\n"+
			"Distance to 200 SMA: %.2f%%\n"+
			"Flagpole: %.2f%% in %d bars (%.2f%% from the 21)\n"+
			"Flag: %d bars, gave back %.0f%% of the pole; the 21 closed %.0f%% of the gap\n"+
			"21 SMA slope: %.3f%% over %d bars\n"+
			"Regime: %d bars since the 21/200 cross\n"+
			"Price tested the 21 SMA without closing through it.",
		from, cfg.Timeframe,
		displaySymbol(cfg.Symbol), c.bar.Close,
		c.fast, c.slow,
		roomPct,
		s.poleMovePct, s.poleBars, s.poleExtPct,
		s.flagBars, s.retracePct, s.catchUpPct,
		math.Abs(s.slopePct), cfg.SlopeBars,
		barsSinceCross)
}

// buildInvalidation renders the optional note sent when price reaches the 200 SMA
// and the setup is invalidated (EmitInvalidation).
func buildInvalidation(cfg Config, regime int, c barCtx) string {
	return fmt.Sprintf(
		"📐 SMA RETEST — PATTERN INVALIDATED (%s)\n"+
			"%s  @ %.2f\n"+
			"Pullback reached the 200 SMA (%.2f); pattern disarmed until the next cross.",
		cfg.Timeframe, displaySymbol(cfg.Symbol), c.bar.Close, c.slow)
}

// displaySymbol turns an exchange symbol like "BTCUSDT" into the "BTC/USDT" form
// used in the message body. Unknown shapes are returned unchanged.
func displaySymbol(sym string) string {
	if len(sym) > 4 && sym[len(sym)-4:] == "USDT" {
		return sym[:len(sym)-4] + "/USDT"
	}
	return sym
}
