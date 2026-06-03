package volume

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/robmeijerink/MarktPandaBot/internal/aggregator"
)

func fakeFetcher(name string, klines []aggregator.Kline, err error) klineFetcher {
	return klineFetcher{
		name:  name,
		fetch: func(_ *http.Client, _ int) ([]aggregator.Kline, error) { return klines, err },
	}
}

func TestAggregatedBucketVolSumsBothVenues(t *testing.T) {
	bucket := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	other := bucket.Add(-5 * time.Minute)

	fetchers := []klineFetcher{
		fakeFetcher("bybit", []aggregator.Kline{
			{BucketStart: other, QuoteVol: 999},
			{BucketStart: bucket, QuoteVol: 100},
		}, nil),
		fakeFetcher("okx", []aggregator.Kline{
			{BucketStart: bucket, QuoteVol: 250},
		}, nil),
	}

	sum, matched := aggregatedBucketVol(fetchers, nil, bucket)
	if matched != 2 {
		t.Fatalf("matched = %d, want 2", matched)
	}
	if sum != 350 {
		t.Fatalf("sum = %v, want 350 (100+250, ignoring the other bucket)", sum)
	}
}

func TestAggregatedBucketVolSkipsErroringVenue(t *testing.T) {
	bucket := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	fetchers := []klineFetcher{
		fakeFetcher("bybit", nil, errors.New("network down")),
		fakeFetcher("okx", []aggregator.Kline{{BucketStart: bucket, QuoteVol: 250}}, nil),
	}

	sum, matched := aggregatedBucketVol(fetchers, nil, bucket)
	if matched != 1 || sum != 250 {
		t.Fatalf("got sum=%v matched=%d, want 250 / 1 (one venue still counts)", sum, matched)
	}
}

func TestAggregatedBucketVolNoMatch(t *testing.T) {
	bucket := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	miss := bucket.Add(-time.Hour)
	fetchers := []klineFetcher{
		fakeFetcher("bybit", []aggregator.Kline{{BucketStart: miss, QuoteVol: 100}}, nil),
	}
	sum, matched := aggregatedBucketVol(fetchers, nil, bucket)
	if matched != 0 || sum != 0 {
		t.Fatalf("got sum=%v matched=%d, want 0 / 0 (caller skips the cycle)", sum, matched)
	}
}
