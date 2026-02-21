package observability

import (
	"sync"
	"testing"
)

func TestMetricsSnapshotCountsIncrements(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics()
	metrics.IncCacheHit()
	metrics.IncCacheMiss()
	metrics.IncRevalidateRun()
	metrics.IncUpstreamRequest()
	metrics.IncUpstreamRequest()
	metrics.IncUpstreamError()

	snapshot := metrics.Snapshot()
	if snapshot.CacheHits != 1 {
		t.Fatalf("expected cache hits to be 1, got %d", snapshot.CacheHits)
	}
	if snapshot.CacheMisses != 1 {
		t.Fatalf("expected cache misses to be 1, got %d", snapshot.CacheMisses)
	}
	if snapshot.RevalidateRuns != 1 {
		t.Fatalf("expected revalidate runs to be 1, got %d", snapshot.RevalidateRuns)
	}
	if snapshot.UpstreamRequests != 2 {
		t.Fatalf("expected upstream requests to be 2, got %d", snapshot.UpstreamRequests)
	}
	if snapshot.UpstreamErrors != 1 {
		t.Fatalf("expected upstream errors to be 1, got %d", snapshot.UpstreamErrors)
	}
}

func TestMetricsConcurrentIncrementsAreSafe(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics()

	const goroutines = 12
	const iterations = 250

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				metrics.IncCacheHit()
				metrics.IncCacheMiss()
				metrics.IncRevalidateRun()
				metrics.IncUpstreamRequest()
				metrics.IncUpstreamError()
			}
		}()
	}

	wg.Wait()

	want := uint64(goroutines * iterations)
	snapshot := metrics.Snapshot()
	if snapshot.CacheHits != want {
		t.Fatalf("expected cache hits to be %d, got %d", want, snapshot.CacheHits)
	}
	if snapshot.CacheMisses != want {
		t.Fatalf("expected cache misses to be %d, got %d", want, snapshot.CacheMisses)
	}
	if snapshot.RevalidateRuns != want {
		t.Fatalf("expected revalidate runs to be %d, got %d", want, snapshot.RevalidateRuns)
	}
	if snapshot.UpstreamRequests != want {
		t.Fatalf("expected upstream requests to be %d, got %d", want, snapshot.UpstreamRequests)
	}
	if snapshot.UpstreamErrors != want {
		t.Fatalf("expected upstream errors to be %d, got %d", want, snapshot.UpstreamErrors)
	}
}
