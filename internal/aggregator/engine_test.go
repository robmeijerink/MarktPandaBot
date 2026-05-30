package aggregator

import (
	"sync"
	"testing"
)

func TestAggregator_Concurrency(t *testing.T) {
	aggr := &Aggregator{}
	var wg sync.WaitGroup

	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aggr.AddEvent(LiquidationEvent{
				Exchange: "binance",
				Symbol:   "BTCUSDT",
				Price:    74000.50,
				Qty:      1.5,
				Side:     "SELL",
			})
		}()
	}
	wg.Wait()

	events := aggr.ExtractAndClear()
	if len(events) != 1000 {
		t.Fatalf("expected 1000 events, got %d", len(events))
	}

	emptyEvents := aggr.ExtractAndClear()
	if len(emptyEvents) != 0 {
		t.Fatalf("expected 0 events after clear, got %d", len(emptyEvents))
	}
}

func TestMarketState_ThreadSafety(t *testing.T) {
	state := &MarketState{}
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		state.Mu.Lock()
		state.BinanceFunding = 0.01
		state.Mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		state.Mu.RLock()
		_ = state.BinanceFunding
		state.Mu.RUnlock()
	}()

	wg.Wait()
}
