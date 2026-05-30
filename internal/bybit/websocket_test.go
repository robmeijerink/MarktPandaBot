package bybit

import (
	"encoding/json"
	"testing"
)

func TestBybitLiquidationUnmarshal(t *testing.T) {
	payload := []byte(`{"data":{"symbol":"BTCUSDT","side":"Buy","price":"74000.00","size":"1.5"}}`)
	var event BybitLiquidation

	err := json.Unmarshal(payload, &event)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if event.Data.Symbol != "BTCUSDT" {
		t.Errorf("expected BTCUSDT, got %s", event.Data.Symbol)
	}
}

func TestBybitTickerUnmarshal(t *testing.T) {
	payload := []byte(`{"topic":"tickers.BTCUSDT","data":{"symbol":"BTCUSDT","fundingRate":"0.0001","openInterest":"1500.50"}}`)
	var event BybitTicker

	err := json.Unmarshal(payload, &event)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if event.Topic != "tickers.BTCUSDT" {
		t.Errorf("expected tickers.BTCUSDT, got %s", event.Topic)
	}

	if event.Data.OpenInterest != "1500.50" {
		t.Errorf("expected 1500.50, got %s", event.Data.OpenInterest)
	}
}
