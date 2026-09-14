package sweep

import (
	"bufio"
	"bytes"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// loadFixture reads real Bybit BTCUSDT 5m candles: start_ms,open,high,low,close,volume.
func loadFixture(t *testing.T, path string) []Bar {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var bars []Bar
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ",")
		ms, err := strconv.ParseInt(p[0], 10, 64)
		if err != nil || len(p) != 6 {
			t.Fatalf("bad fixture row %q", sc.Text())
		}
		v := make([]float64, 5)
		for i := range v {
			if v[i], err = strconv.ParseFloat(p[i+1], 64); err != nil {
				t.Fatalf("bad fixture row %q", sc.Text())
			}
		}
		bars = append(bars, Bar{Start: time.UnixMilli(ms).UTC(), Open: v[0], High: v[1], Low: v[2], Close: v[3], Volume: v[4]})
	}
	return bars
}

// A real BTC sweep: on 2026-09-09 the 20:25 UTC 5m candle ran the 1h swing low at
// $78,019 (formed five hours earlier) and closed back above it with a large wick on
// heavy volume. The 60 hours before it are replayed exactly like a warm boot; the
// sweep candle then arrives live. Liquidation history does not exist, so the burst is
// injected: with it the module alerts on that exact level, without it it stays silent.
func TestRealBTCSweep(t *testing.T) {
	bars := loadFixture(t, "testdata/btcusdt_5m_2026-09-09.csv")
	sweepBar := bars[len(bars)-1]
	if want := time.Date(2026, 9, 9, 20, 25, 0, 0, time.UTC); !sweepBar.Start.Equal(want) {
		t.Fatalf("fixture should end with the %v sweep candle, got %v", want, sweepBar.Start)
	}

	run := func(burst bool) ([]string, string) {
		cfg := DefaultConfig()
		cfg.OutcomeHorizonsMin = nil
		var sent []string
		liq := newLiqStore()
		d := newDetector(cfg, liq, func(msg string) { sent = append(sent, msg) })
		replay(d, bars[:len(bars)-1])

		liq.setUp(venueBybit, true, sweepBar.Start.Add(-time.Hour))
		liq.setUp(venueOKX, true, sweepBar.Start.Add(-time.Hour))
		if burst {
			at := sweepBar.Start.Add(2 * time.Minute)
			liq.add(
				liqEvent{at: at, venue: venueBybit, long: true, usd: 480000},
				liqEvent{at: at, venue: venueOKX, long: true, usd: 120000},
				liqEvent{at: at, venue: venueBybit, long: false, usd: 30000},
			)
		}
		var logs bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(prev)
		d.processBar(sweepBar)
		return sent, logs.String()
	}

	sent, logs := run(true)
	if len(sent) != 1 {
		t.Fatalf("the real sweep with a liquidation burst must alert once, got %d; log:\n%s", len(sent), logs)
	}
	for _, want := range []string{
		"🟢 *BULLISH*",
		"🎯 Swept *1h swing low* $78,019 (formed 5h ago)",
		"5m candle closed back above",
		"*$600k longs liquidated* (Bybit $480k · OKX $120k)",
	} {
		if !strings.Contains(sent[0], want) {
			t.Errorf("alert is missing %q:\n%s", want, sent[0])
		}
	}

	sent, logs = run(false)
	if len(sent) != 0 || !strings.Contains(logs, "longs liquidated $0 (min $250k)") {
		t.Fatalf("the same candle without liquidations must be rejected for that reason: sent=%d log:\n%s", len(sent), logs)
	}
}
