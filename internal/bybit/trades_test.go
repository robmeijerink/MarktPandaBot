package bybit

import (
	"testing"
	"time"
)

func TestDecodeBybitTradesSigns(t *testing.T) {
	msg := []byte(`{"topic":"publicTrade.BTCUSDT","data":[
		{"T":1700000000000,"S":"Buy","v":"0.5","p":"60000"},
		{"T":1700000000000,"S":"Sell","v":"0.2","p":"60000"}
	]}`)

	flows := decodeBybitTrades(msg)
	if len(flows) != 2 {
		t.Fatalf("len = %d, want 2", len(flows))
	}
	// taker-buy is positive notional
	if flows[0].usd != 0.5*60000 {
		t.Fatalf("buy usd = %v, want %v", flows[0].usd, 0.5*60000)
	}
	// taker-sell is negative notional
	if flows[1].usd != -0.2*60000 {
		t.Fatalf("sell usd = %v, want %v", flows[1].usd, -0.2*60000)
	}
	if !flows[0].ts.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Fatalf("ts = %v, want %v", flows[0].ts, time.UnixMilli(1700000000000).UTC())
	}
}

func TestDecodeBybitTradesEmptyOrBad(t *testing.T) {
	if got := decodeBybitTrades([]byte(`{"topic":"x","data":[]}`)); got != nil {
		t.Fatalf("empty data -> %v, want nil", got)
	}
	if got := decodeBybitTrades([]byte(`not json`)); got != nil {
		t.Fatalf("bad json -> %v, want nil", got)
	}
}
