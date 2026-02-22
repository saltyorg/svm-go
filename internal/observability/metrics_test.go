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
	metrics.IncRefreshUpdated()
	metrics.IncRefreshNotMod()
	metrics.IncRefreshSkipped()
	metrics.IncRefreshFailed()
	metrics.IncRefreshProcessed()
	metrics.ObserveRateLimitRemaining(1234)

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
	if snapshot.RefreshUpdated != 1 {
		t.Fatalf("expected refresh updated to be 1, got %d", snapshot.RefreshUpdated)
	}
	if snapshot.RefreshNotMod != 1 {
		t.Fatalf("expected refresh not-modified to be 1, got %d", snapshot.RefreshNotMod)
	}
	if snapshot.RefreshSkipped != 1 {
		t.Fatalf("expected refresh skipped to be 1, got %d", snapshot.RefreshSkipped)
	}
	if snapshot.RefreshFailed != 1 {
		t.Fatalf("expected refresh failed to be 1, got %d", snapshot.RefreshFailed)
	}
	if snapshot.RefreshProcessed != 1 {
		t.Fatalf("expected refresh processed to be 1, got %d", snapshot.RefreshProcessed)
	}
	if snapshot.RateLimitRemain != 1234 {
		t.Fatalf("expected rate-limit remaining to be 1234, got %d", snapshot.RateLimitRemain)
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
				metrics.IncRefreshUpdated()
				metrics.IncRefreshNotMod()
				metrics.IncRefreshSkipped()
				metrics.IncRefreshFailed()
				metrics.IncRefreshProcessed()
				metrics.ObserveRateLimitRemaining(5000)
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
	if snapshot.RefreshUpdated != want {
		t.Fatalf("expected refresh updated to be %d, got %d", want, snapshot.RefreshUpdated)
	}
	if snapshot.RefreshNotMod != want {
		t.Fatalf("expected refresh not-modified to be %d, got %d", want, snapshot.RefreshNotMod)
	}
	if snapshot.RefreshSkipped != want {
		t.Fatalf("expected refresh skipped to be %d, got %d", want, snapshot.RefreshSkipped)
	}
	if snapshot.RefreshFailed != want {
		t.Fatalf("expected refresh failed to be %d, got %d", want, snapshot.RefreshFailed)
	}
	if snapshot.RefreshProcessed != want {
		t.Fatalf("expected refresh processed to be %d, got %d", want, snapshot.RefreshProcessed)
	}
	if snapshot.RateLimitRemain != 5000 {
		t.Fatalf("expected rate-limit remaining to be 5000, got %d", snapshot.RateLimitRemain)
	}
}

func TestMetricsRateLimitRemainingDefaultsToUnknownAndClampsNegative(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics()
	if got := metrics.Snapshot().RateLimitRemain; got != -1 {
		t.Fatalf("expected default rate-limit remaining -1, got %d", got)
	}

	metrics.ObserveRateLimitRemaining(-5)
	if got := metrics.Snapshot().RateLimitRemain; got != 0 {
		t.Fatalf("expected clamped rate-limit remaining 0, got %d", got)
	}
}
