package bybit

import (
	"encoding/json"
	"testing"
)

func TestBybitLiquidationUnmarshal(t *testing.T) {
	payload := []byte(`{"topic":"allLiquidation.BTCUSDT","data":[{"T":1739502302929,"s":"BTCUSDT","S":"Buy","v":"1.5","p":"74000.00"}]}`)
	var event BybitLiquidation

	err := json.Unmarshal(payload, &event)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(event.Data) != 1 {
		t.Fatalf("expected 1 liquidation entry, got %d", len(event.Data))
	}

	if event.Data[0].Symbol != "BTCUSDT" {
		t.Errorf("expected BTCUSDT, got %s", event.Data[0].Symbol)
	}
}

func TestBybitTickerUnmarshal(t *testing.T) {
	payload := []byte(`{"topic":"tickers.BTCUSDT","data":{"symbol":"BTCUSDT","fundingRate":"0.0001","openInterestValue":"4286132155.70"}}`)
	var event BybitTicker

	err := json.Unmarshal(payload, &event)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if event.Topic != "tickers.BTCUSDT" {
		t.Errorf("expected tickers.BTCUSDT, got %s", event.Topic)
	}

	if event.Data.OpenInterestValue != "4286132155.70" {
		t.Errorf("expected 4286132155.70, got %s", event.Data.OpenInterestValue)
	}
}
