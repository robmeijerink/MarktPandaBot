package binance

import (
	"encoding/json"
	"testing"
)

func TestBinanceForceOrderUnmarshal(t *testing.T) {
	payload := []byte(`{"o":{"s":"BTCUSDT","S":"SELL","p":"74500.00","q":"5.2"}}`)
	var event BinanceForceOrder

	err := json.Unmarshal(payload, &event)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if event.Order.Symbol != "BTCUSDT" {
		t.Errorf("expected BTCUSDT, got %s", event.Order.Symbol)
	}

	if event.Order.Price != "74500.00" {
		t.Errorf("expected 74500.00, got %s", event.Order.Price)
	}
}

func TestBinanceMarkPriceUnmarshal(t *testing.T) {
	payload := []byte(`{"s":"BTCUSDT","r":"0.00010000"}`)
	var event BinanceMarkPrice

	err := json.Unmarshal(payload, &event)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if event.FundingRate != "0.00010000" {
		t.Errorf("expected 0.00010000, got %s", event.FundingRate)
	}
}
