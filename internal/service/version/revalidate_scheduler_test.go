package version

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
)

func TestRevalidateSchedulerSweepFiltersDueKeysAndUsesLookback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 30, 0, 0, time.UTC)
	source := &fakeActiveKeySource{
		keys: []string{"key-due", "key-fresh", "key-missing"},
	}
	store := &fakeSchedulerCacheStore{
		records: map[string]cache.Record{
			"key-due": {
				LastCheckedAt: now.Add(-(2 * time.Minute)),
			},
			"key-fresh": {
				LastCheckedAt: now.Add(-10 * time.Second),
			},
		},
	}
	queue := &fakeRefreshEnqueuer{}

	policy := cache.DefaultPolicy()
	policy.RevalidateInterval = time.Minute
	policy.RevalidateLookback = 7 * 24 * time.Hour
	policy.RevalidateEndpointsPerWorker = 30

	scheduler := newRevalidateScheduler(
		source,
		store,
		queue,
		policy,
		nil,
		func() time.Time { return now },
		false,
		nil,
	)

	result := scheduler.Sweep(context.Background())
	if result.CandidateKeys != 3 {
		t.Fatalf("expected 3 candidate keys, got %d", result.CandidateKeys)
	}
	if result.DueKeys != 1 {
		t.Fatalf("expected 1 due key, got %d", result.DueKeys)
	}
	if result.NotStaleKeys != 1 {
		t.Fatalf("expected 1 not-stale key, got %d", result.NotStaleKeys)
	}
	if result.WorkerCount != 1 {
		t.Fatalf("expected worker count 1, got %d", result.WorkerCount)
	}
	if result.Enqueued != 1 {
		t.Fatalf("expected 1 enqueued key, got %d", result.Enqueued)
	}
	if result.Dropped != 0 {
		t.Fatalf("expected 0 dropped keys, got %d", result.Dropped)
	}
	if len(queue.keys) != 1 || queue.keys[0] != "key-due" {
		t.Fatalf("expected enqueued keys %v, got %v", []string{"key-due"}, queue.keys)
	}

	expectedSince := now.Add(-policy.RevalidateLookback)
	if !source.lastSince.Equal(expectedSince) {
		t.Fatalf("expected lookback since %v, got %v", expectedSince, source.lastSince)
	}
}

func TestRevalidateSchedulerWorkerSizingAndPartitioning(t *testing.T) {
	t.Parallel()

	dueKeys := make([]string, 0, 65)
	for i := range 65 {
		dueKeys = append(dueKeys, "key-"+time.Unix(int64(i), 0).UTC().Format("150405"))
	}

	source := &fakeActiveKeySource{keys: dueKeys}
	store := &fakeSchedulerCacheStore{
		records: make(map[string]cache.Record, len(dueKeys)),
	}
	for _, key := range dueKeys {
		store.records[key] = cache.Record{LastCheckedAt: time.Time{}}
	}
	queue := &fakeRefreshEnqueuer{}

	policy := cache.DefaultPolicy()
	policy.RevalidateInterval = time.Minute
	policy.RevalidateEndpointsPerWorker = 30

	scheduler := newRevalidateScheduler(
		source,
		store,
		queue,
		policy,
		nil,
		time.Now,
		false,
		nil,
	)

	result := scheduler.Sweep(context.Background())
	if result.DueKeys != 65 {
		t.Fatalf("expected 65 due keys, got %d", result.DueKeys)
	}
	if result.WorkerCount != 3 {
		t.Fatalf("expected worker count 3, got %d", result.WorkerCount)
	}
	if result.Enqueued != 65 {
		t.Fatalf("expected 65 enqueued keys, got %d", result.Enqueued)
	}

	partitions := partitionRefreshKeys(dueKeys, 30)
	if len(partitions) != 3 {
		t.Fatalf("expected 3 partitions, got %d", len(partitions))
	}
	for _, partition := range partitions {
		if len(partition) > 30 {
			t.Fatalf("expected each partition size <= 30, got %d", len(partition))
		}
	}
}

func TestWorkerCountForDueEndpointsAlwaysAtLeastOne(t *testing.T) {
	t.Parallel()

	if got := workerCountForDueEndpoints(0, 30); got != 1 {
		t.Fatalf("expected 1 worker for 0 due endpoints, got %d", got)
	}
	if got := workerCountForDueEndpoints(31, 30); got != 2 {
		t.Fatalf("expected 2 workers for 31 due endpoints, got %d", got)
	}
}

func TestNewRevalidateSchedulerDefaultsIntervalToOneMinute(t *testing.T) {
	t.Parallel()

	scheduler := newRevalidateScheduler(
		&fakeActiveKeySource{},
		&fakeSchedulerCacheStore{},
		&fakeRefreshEnqueuer{},
		cache.Policy{},
		nil,
		time.Now,
		false,
		nil,
	)

	if scheduler.interval != time.Minute {
		t.Fatalf("expected default interval %s, got %s", time.Minute, scheduler.interval)
	}
}

func TestRevalidateSchedulerRunSweepsOnTicks(t *testing.T) {
	t.Parallel()

	tickCh := make(chan time.Time, 2)
	source := &fakeActiveKeySource{}
	store := &fakeSchedulerCacheStore{}
	queue := &fakeRefreshEnqueuer{}

	scheduler := newRevalidateScheduler(
		source,
		store,
		queue,
		cache.DefaultPolicy(),
		nil,
		time.Now,
		true,
		tickCh,
	)
	defer scheduler.Close()

	tickCh <- time.Now()
	tickCh <- time.Now()

	deadline := time.After(time.Second)
	for {
		if source.callCount() >= 2 {
			break
		}

		select {
		case <-deadline:
			t.Fatalf("expected at least 2 sweeps, got %d", source.callCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestRevalidateSchedulerDropsWhenQueueIsFull(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 35, 0, 0, time.UTC)
	source := &fakeActiveKeySource{
		keys: []string{"key-1", "key-2"},
	}
	store := &fakeSchedulerCacheStore{
		records: map[string]cache.Record{
			"key-1": {LastCheckedAt: now.Add(-time.Minute)},
			"key-2": {LastCheckedAt: now.Add(-time.Minute)},
		},
	}
	queue := NewRefreshQueue(1)
	defer queue.Close()

	scheduler := newRevalidateScheduler(
		source,
		store,
		queue,
		cache.DefaultPolicy(),
		nil,
		func() time.Time { return now },
		false,
		nil,
	)

	start := time.Now()
	result := scheduler.Sweep(context.Background())
	elapsed := time.Since(start)

	if result.Enqueued != 1 {
		t.Fatalf("expected 1 enqueued key, got %d", result.Enqueued)
	}
	if result.Dropped != 1 {
		t.Fatalf("expected 1 dropped key, got %d", result.Dropped)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected non-blocking enqueue path, sweep took %s", elapsed)
	}
}

func TestRevalidateSchedulerSweepIncrementsRunMetric(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 45, 0, 0, time.UTC)
	metrics := observability.NewMetrics()
	scheduler := newRevalidateScheduler(
		&fakeActiveKeySource{},
		&fakeSchedulerCacheStore{},
		&fakeRefreshEnqueuer{},
		cache.DefaultPolicy(),
		nil,
		func() time.Time { return now },
		false,
		nil,
	)
	scheduler.SetMetrics(metrics)

	_ = scheduler.Sweep(context.Background())

	if got := metrics.Snapshot().RevalidateRuns; got != 1 {
		t.Fatalf("expected 1 revalidation run, got %d", got)
	}
}

func TestRevalidateSchedulerFinishSweepLogsRefreshDeltas(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 47, 0, 0, time.UTC)
	var logs bytes.Buffer
	logger := observability.NewLogger(&logs)
	metrics := observability.NewMetrics()

	scheduler := newRevalidateScheduler(
		&fakeActiveKeySource{},
		&fakeSchedulerCacheStore{},
		&fakeRefreshEnqueuer{},
		cache.DefaultPolicy(),
		logger,
		func() time.Time { return now },
		false,
		nil,
	)
	scheduler.SetMetrics(metrics)

	metrics.IncUpstreamRequest()
	metrics.IncUpstreamError()
	metrics.IncRefreshUpdated()
	metrics.IncRefreshNotMod()
	metrics.IncRefreshSkipped()
	metrics.IncRefreshFailed()

	scheduler.finishSweep(RevalidateSweepResult{})

	output := logs.String()
	if !strings.Contains(output, `"cycle_refresh_updated":1`) {
		t.Fatalf("expected log to include refresh updated delta, got %q", output)
	}
	if !strings.Contains(output, `"cycle_refresh_not_modified":1`) {
		t.Fatalf("expected log to include refresh not-modified delta, got %q", output)
	}
	if !strings.Contains(output, `"cycle_refresh_skipped":1`) {
		t.Fatalf("expected log to include refresh skipped delta, got %q", output)
	}
	if !strings.Contains(output, `"cycle_refresh_failed":1`) {
		t.Fatalf("expected log to include refresh failed delta, got %q", output)
	}
}

func TestRevalidateSchedulerSweepUpdatesWorkerScaler(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 50, 0, 0, time.UTC)
	dueKeys := make([]string, 0, 65)
	for i := range 65 {
		dueKeys = append(dueKeys, "key-"+time.Unix(int64(i), 0).UTC().Format("150405"))
	}

	source := &fakeActiveKeySource{keys: dueKeys}
	store := &fakeSchedulerCacheStore{
		records: make(map[string]cache.Record, len(dueKeys)),
	}
	for _, key := range dueKeys {
		store.records[key] = cache.Record{LastCheckedAt: time.Time{}}
	}

	policy := cache.DefaultPolicy()
	policy.RevalidateEndpointsPerWorker = 30
	scheduler := newRevalidateScheduler(
		source,
		store,
		&fakeRefreshEnqueuer{},
		policy,
		nil,
		func() time.Time { return now },
		false,
		nil,
	)
	scaler := &fakeRefreshWorkerScaler{}
	scheduler.SetWorkerScaler(scaler)

	result := scheduler.Sweep(context.Background())
	if result.WorkerCount != 3 {
		t.Fatalf("expected worker count 3, got %d", result.WorkerCount)
	}
	if scaler.callCount != 1 {
		t.Fatalf("expected scaler to be called once, got %d", scaler.callCount)
	}
	if scaler.lastCount != 3 {
		t.Fatalf("expected scaler target 3 workers, got %d", scaler.lastCount)
	}
}

type fakeActiveKeySource struct {
	mu        sync.Mutex
	keys      []string
	lastSince time.Time
	calls     int
}

func (f *fakeActiveKeySource) ActiveKeysSince(
	_ context.Context,
	since time.Time,
	_ int,
) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	f.lastSince = since
	return append([]string(nil), f.keys...), nil
}

func (f *fakeActiveKeySource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeSchedulerCacheStore struct {
	records map[string]cache.Record
}

func (f *fakeSchedulerCacheStore) Get(_ context.Context, key string) (cache.Record, bool, error) {
	record, ok := f.records[key]
	if !ok {
		return cache.Record{}, false, nil
	}

	return record, true, nil
}

func (f *fakeSchedulerCacheStore) Set(string, cache.Record) (bool, error) {
	return true, nil
}

type fakeRefreshEnqueuer struct {
	keys []string
}

func (f *fakeRefreshEnqueuer) Enqueue(key string) bool {
	f.keys = append(f.keys, key)
	return true
}

type fakeRefreshWorkerScaler struct {
	callCount int
	lastCount int
}

func (f *fakeRefreshWorkerScaler) SetWorkerCount(workerCount int) {
	f.callCount++
	f.lastCount = workerCount
}
