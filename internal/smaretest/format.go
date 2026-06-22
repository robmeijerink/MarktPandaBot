package smaretest

import (
	"fmt"
	"math"
)

// buildTouch renders the independent SMA-retest alert (§5). It uses a distinct
// 📐 prefix so the feed stays readable next to the existing alerts, and is sent
// as a brand-new message — it never reuses or appends to existing strings.
func buildTouch(cfg Config, regime int, c barCtx, barsSinceCross int) string {
	roomPct := math.Abs(c.bar.Close-c.slow) / c.bar.Close * 100
	if regime == regimeLong {
		return fmt.Sprintf(
			"📐 SMA RETEST — LONG (%s)\n"+
				"%s  @ %.2f\n"+
				"21 SMA: %.2f   |   200 SMA: %.2f\n"+
				"Touch low: %.2f\n"+
				"Room to 200 SMA: %.2f%%\n"+
				"Moved %.2f%% away from the 21 since the cross\n"+
				"Range: tight (%.2f%% over %d bars)\n"+
				"Regime: %d bars since golden cross\n"+
				"Move-away + tight-range retest of the 21 SMA (support held) — model entry.",
			cfg.Timeframe, displaySymbol(cfg.Symbol), c.bar.Close,
			c.fast, c.slow, c.bar.Low, roomPct, c.sepPct, c.flagRangePct, cfg.FlagLookback, barsSinceCross)
	}
	return fmt.Sprintf(
		"📐 SMA RETEST — SHORT (%s)\n"+
			"%s  @ %.2f\n"+
			"21 SMA: %.2f   |   200 SMA: %.2f\n"+
			"Touch high: %.2f\n"+
			"Room to 200 SMA: %.2f%%\n"+
			"Moved %.2f%% away from the 21 since the cross\n"+
			"Range: tight (%.2f%% over %d bars)\n"+
			"Regime: %d bars since death cross\n"+
			"Move-away + tight-range retest of the 21 SMA (resistance held) — model entry.",
		cfg.Timeframe, displaySymbol(cfg.Symbol), c.bar.Close,
		c.fast, c.slow, c.bar.High, roomPct, c.sepPct, c.flagRangePct, cfg.FlagLookback, barsSinceCross)
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
