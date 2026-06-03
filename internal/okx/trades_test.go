package okx

import (
	"testing"
)

func TestDecodeOKXTradesPerpSpotAndSigns(t *testing.T) {
	// Perp sizes are in contracts (×0.01 BTC); spot sizes are in base (BTC).
	msg := []byte(`{"arg":{"channel":"trades"},"data":[
		{"instId":"BTC-USDT-SWAP","px":"60000","sz":"10","side":"buy","ts":"1700000000000"},
		{"instId":"BTC-USDT-SWAP","px":"60000","sz":"5","side":"sell","ts":"1700000000000"},
		{"instId":"BTC-USDT","px":"60000","sz":"0.1","side":"buy","ts":"1700000000000"},
		{"instId":"ETH-USDT","px":"3000","sz":"1","side":"buy","ts":"1700000000000"}
	]}`)

	flows := decodeOKXTrades(msg)
	if len(flows) != 3 {
		t.Fatalf("len = %d, want 3 (ETH ignored)", len(flows))
	}

	// perp buy: 10 contracts * 0.01 * 60000 = 6000, perp=true
	if !flows[0].perp || flows[0].usd != 6000 {
		t.Fatalf("perp buy = {perp:%v usd:%v}, want {true 6000}", flows[0].perp, flows[0].usd)
	}
	// perp sell: -(5 * 0.01 * 60000) = -3000
	if !flows[1].perp || flows[1].usd != -3000 {
		t.Fatalf("perp sell = {perp:%v usd:%v}, want {true -3000}", flows[1].perp, flows[1].usd)
	}
	// spot buy: 0.1 * 60000 = 6000, perp=false
	if flows[2].perp || flows[2].usd != 6000 {
		t.Fatalf("spot buy = {perp:%v usd:%v}, want {false 6000}", flows[2].perp, flows[2].usd)
	}
}

func TestDecodeOKXTradesEmptyOrBad(t *testing.T) {
	if got := decodeOKXTrades([]byte(`{"data":[]}`)); got != nil {
		t.Fatalf("empty -> %v, want nil", got)
	}
	if got := decodeOKXTrades([]byte(`garbage`)); got != nil {
		t.Fatalf("bad json -> %v, want nil", got)
	}
}
