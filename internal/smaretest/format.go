package smaretest

import (
	"fmt"
	"math"
)

// buildTouch renders the independent SMA-retest alert (§5). It uses a distinct
// 📐 prefix so the feed stays readable next to the existing alerts, and is sent
// as a brand-new message — it never reuses or appends to existing strings.
func buildTouch(cfg Config, regime int, c barCtx, s setupStats, barsSinceCross int) string {
	roomPct := math.Abs(c.bar.Close-c.slow) / c.bar.Close * 100
	side, touchLabel, touch, away, slope, cross, held := "LONG", "Touch low", c.bar.Low, "above", "rising", "golden cross", "support held"
	if regime == regimeShort {
		side, touchLabel, touch, away, slope, cross, held = "SHORT", "Touch high", c.bar.High, "below", "falling", "death cross", "resistance held"
	}
	return fmt.Sprintf(
		"📐 SMA RETEST — %s (%s)\n"+
			"%s  @ %.2f\n"+
			"21 SMA: %.2f   |   200 SMA: %.2f\n"+
			"%s: %.2f\n"+
			"Room to 200 SMA: %.2f%%\n"+
			"Flagpole: %.2f%% in %d bars (%.2f%% %s the 21)\n"+
			"Flag: %d bars, gave back %.0f%% of the pole; the 21 closed %.0f%% of the gap\n"+
			"21 SMA %s: %.3f%% over %d bars\n"+
			"Regime: %d bars since %s\n"+
			"Cross + flagpole + flag + kiss of the %s 21 SMA (%s) — model entry.",
		side, cfg.Timeframe,
		displaySymbol(cfg.Symbol), c.bar.Close,
		c.fast, c.slow,
		touchLabel, touch,
		roomPct,
		s.poleMovePct, s.poleBars, s.poleExtPct, away,
		s.flagBars, s.retracePct, s.catchUpPct,
		slope, s.slopePct, cfg.SlopeBars,
		barsSinceCross, cross,
		slope, held)
}

// buildInvalidation renders the optional note sent when price reaches the 200 SMA
// and the setup is invalidated (EmitInvalidation).
func buildInvalidation(cfg Config, regime int, c barCtx) string {
	side := "LONG"
	if regime == regimeShort {
		side = "SHORT"
	}
	return fmt.Sprintf(
		"📐 SMA RETEST — %s INVALIDATED (%s)\n"+
			"%s  @ %.2f\n"+
			"Pullback reached the 200 SMA (%.2f); setup disarmed until the next cross.",
		side, cfg.Timeframe, displaySymbol(cfg.Symbol), c.bar.Close, c.slow)
}

// displaySymbol turns an exchange symbol like "BTCUSDT" into the "BTC/USDT" form
// used in the message body. Unknown shapes are returned unchanged.
func displaySymbol(sym string) string {
	if len(sym) > 4 && sym[len(sym)-4:] == "USDT" {
		return sym[:len(sym)-4] + "/USDT"
	}
	return sym
}
