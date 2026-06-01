package okx

import (
	"encoding/json"
	"testing"
)

func TestOKXLiquidationUnmarshal(t *testing.T) {
	// Real allLiquidation-orders shape: data[].details[] with sz in contracts.
	payload := []byte(`{"arg":{"channel":"liquidation-orders","instType":"SWAP"},"data":[{"instId":"BTC-USDT-SWAP","details":[{"bkPx":"74000.0","sz":"150","side":"sell"}]}]}`)
	var event OKXLiquidation

	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(event.Data) != 1 || len(event.Data[0].Details) != 1 {
		t.Fatalf("expected 1 data entry with 1 detail, got %d entries", len(event.Data))
	}

	d := event.Data[0]
	if d.InstID != okxBTCInst {
		t.Errorf("expected %s, got %s", okxBTCInst, d.InstID)
	}
	if d.Details[0].Side != "sell" {
		t.Errorf("expected side sell, got %s", d.Details[0].Side)
	}

	// 150 contracts * 0.01 BTC/contract = 1.5 BTC.
	if got := 150.0 * okxBTCContractSize; got != 1.5 {
		t.Errorf("expected 1.5 BTC, got %.4f", got)
	}
}

func TestOKXContextChannels(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		channel string
	}{
		{"funding", `{"arg":{"channel":"funding-rate","instId":"BTC-USDT-SWAP"},"data":[{"fundingRate":"0.0001"}]}`, "funding-rate"},
		{"oi", `{"arg":{"channel":"open-interest","instId":"BTC-USDT-SWAP"},"data":[{"oiUsd":"2563325663.03"}]}`, "open-interest"},
		{"ticker", `{"arg":{"channel":"tickers","instId":"BTC-USDT-SWAP"},"data":[{"last":"71589.6","open24h":"73463.8"}]}`, "tickers"},
	}
	for _, c := range cases {
		var msg okxChannelMsg
		if err := json.Unmarshal([]byte(c.payload), &msg); err != nil {
			t.Fatalf("%s: unmarshal envelope: %v", c.name, err)
		}
		if msg.Arg.Channel != c.channel || len(msg.Data) != 1 {
			t.Fatalf("%s: expected channel %s with 1 data entry, got %s/%d", c.name, c.channel, msg.Arg.Channel, len(msg.Data))
		}
	}

	// Spot-check that the OI payload decodes to a parseable USD figure.
	var msg okxChannelMsg
	_ = json.Unmarshal([]byte(cases[1].payload), &msg)
	var oi okxOIData
	if json.Unmarshal(msg.Data[0], &oi) != nil || oi.OiUsd != "2563325663.03" {
		t.Errorf("expected oiUsd 2563325663.03, got %q", oi.OiUsd)
	}
}
