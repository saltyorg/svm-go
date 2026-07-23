package version

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
)

func TestRevalidateSchedulerSweepFiltersAndEnqueuesDueKeys(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 30, 0, 0, time.UTC)
	source := &schedulerActiveKeys{keys: []string{"due", "fresh", "missing"}}
	store := &schedulerCacheStore{records: map[string]cache.Record{
		"due":   {LastCheckedAt: now.Add(-2 * time.Minute)},
		"fresh": {LastCheckedAt: now.Add(-10 * time.Second)},
	}}
	queue := &schedulerQueue{}
	policy := cache.DefaultPolicy()
	policy.RevalidateInterval = time.Minute
	policy.RevalidateLookback = 7 * 24 * time.Hour
	policy.RevalidateWorkers = 3

	scheduler := newRevalidateScheduler(source, store, queue, policy, nil, func() time.Time { return now }, false, nil)
	result := scheduler.Sweep(context.Background())

	if result.CandidateKeys != 3 || result.DueKeys != 1 || result.NotStaleKeys != 1 {
		t.Fatalf("unexpected sweep result: %+v", result)
	}
	if result.WorkerCount != 3 || result.Enqueued != 1 || result.Dropped != 0 {
		t.Fatalf("unexpected enqueue result: %+v", result)
	}
	if len(queue.keys) != 1 || queue.keys[0] != "due" {
		t.Fatalf("expected due key, got %v", queue.keys)
	}
	if want := now.Add(-policy.RevalidateLookback); !source.since.Equal(want) {
		t.Fatalf("expected lookback %v, got %v", want, source.since)
	}
}

func TestRevalidateSchedulerDropsWhenQueueFull(t *testing.T) {
	t.Parallel()

	source := &schedulerActiveKeys{keys: []string{"a", "b"}}
	store := &schedulerCacheStore{records: map[string]cache.Record{
		"a": {}, "b": {},
	}}
	queue := &schedulerQueue{capacity: 1}
	scheduler := newRevalidateScheduler(source, store, queue, cache.DefaultPolicy(), nil, time.Now, false, nil)

	result := scheduler.Sweep(context.Background())
	if result.Enqueued != 1 || result.Dropped != 1 {
		t.Fatalf("expected one enqueue and one drop, got %+v", result)
	}
}

func TestRevalidateSchedulerSweepIncrementsMetric(t *testing.T) {
	t.Parallel()

	scheduler := newRevalidateScheduler(
		&schedulerActiveKeys{},
		&schedulerCacheStore{},
		&schedulerQueue{},
		cache.DefaultPolicy(),
		observability.NewLogger(io.Discard),
		time.Now,
		false,
		nil,
	)
	metrics := observability.NewMetrics()
	scheduler.SetMetrics(metrics)
	scheduler.Sweep(context.Background())
	if got := metrics.Snapshot().RevalidateRuns; got != 1 {
		t.Fatalf("expected one run, got %d", got)
	}
}

func TestRevalidateSchedulerRunSweepsOnTick(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time, 1)
	source := &schedulerActiveKeys{}
	scheduler := newRevalidateScheduler(
		source,
		&schedulerCacheStore{},
		&schedulerQueue{},
		cache.DefaultPolicy(),
		nil,
		time.Now,
		true,
		ticks,
	)
	ticks <- time.Now()
	deadline := time.After(time.Second)
	for source.calls() == 0 {
		select {
		case <-deadline:
			t.Fatal("scheduler did not sweep")
		case <-time.After(time.Millisecond):
		}
	}
	scheduler.Close()
}

type schedulerActiveKeys struct {
	mu    sync.Mutex
	keys  []string
	since time.Time
	count int
}

func (s *schedulerActiveKeys) ActiveKeysSince(_ context.Context, since time.Time, _ int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.since = since
	s.count++
	return append([]string(nil), s.keys...), nil
}

func (s *schedulerActiveKeys) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

type schedulerCacheStore struct {
	records map[string]cache.Record
}

func (s *schedulerCacheStore) Get(_ context.Context, key string) (cache.Record, bool, error) {
	record, ok := s.records[key]
	return record, ok, nil
}

func (s *schedulerCacheStore) Set(string, cache.Record) (bool, error) {
	return true, nil
}

type schedulerQueue struct {
	keys     []string
	capacity int
}

func (q *schedulerQueue) Enqueue(job RefreshJob) bool {
	if q.capacity > 0 && len(q.keys) >= q.capacity {
		return false
	}
	q.keys = append(q.keys, job.Key)
	return true
}
