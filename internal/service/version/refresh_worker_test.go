package version

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
	upstreamgithub "svm/internal/upstream/github"
)

func TestRefreshWorkersProcessQueuedKeys(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(4)
	defer queue.Close()

	refresher := newFakeRefreshByKeyRequester()
	workers := newRefreshWorkers(queue, refresher, 1, 1000, nil, time.Now, true)
	metrics := observability.NewMetrics()
	workers.SetMetrics(metrics)
	defer workers.Close()

	if ok := queue.Enqueue("key-1"); !ok {
		t.Fatal("expected first key enqueue to succeed")
	}
	if ok := queue.Enqueue("key-2"); !ok {
		t.Fatal("expected second key enqueue to succeed")
	}

	refresher.waitForCalls(t, 2, time.Second)
	if calls := refresher.callCount(); calls != 2 {
		t.Fatalf("expected 2 refresh calls, got %d", calls)
	}
	if got := metrics.Snapshot().RefreshProcessed; got != 2 {
		t.Fatalf("expected 2 processed refresh jobs, got %d", got)
	}
}

func TestRefreshWorkersSetWorkerCountScalesUp(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(8)
	defer queue.Close()

	refresher := newBlockingRefreshByKeyRequester()
	workers := newRefreshWorkers(queue, refresher, 1, 1000, nil, time.Now, true)
	defer workers.Close()

	if ok := queue.Enqueue("key-1"); !ok {
		t.Fatal("expected enqueue for key-1 to succeed")
	}
	if ok := queue.Enqueue("key-2"); !ok {
		t.Fatal("expected enqueue for key-2 to succeed")
	}
	if ok := queue.Enqueue("key-3"); !ok {
		t.Fatal("expected enqueue for key-3 to succeed")
	}

	refresher.waitForStarted(t, 1, time.Second)
	workers.SetWorkerCount(3)
	refresher.waitForStarted(t, 3, time.Second)

	refresher.releaseAll()
	refresher.waitForCompleted(t, 3, time.Second)
}

func TestRefreshWorkersWithServiceDedupesInFlightKey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 40, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       []byte(`{"tag_name":"cached"}`),
			ETag:          `"etag-cached"`,
			URL:           rawURL,
			LastCheckedAt: now.Add(-time.Minute),
			SourceStatus:  http.StatusOK,
		},
		hit: true,
	}
	upstream := newBlockingUpstreamClient(http.StatusNotModified, "")
	service := NewService(
		cacheStore,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	queue := NewRefreshQueue(4)
	defer queue.Close()
	workers := newRefreshWorkers(queue, service, 1, 1000, nil, time.Now, true)
	defer workers.Close()

	if ok := queue.Enqueue(cacheKey); !ok {
		t.Fatal("expected first key enqueue to succeed")
	}
	if ok := queue.Enqueue(cacheKey); !ok {
		t.Fatal("expected duplicate key enqueue to succeed")
	}

	upstream.waitStarted(t)
	time.Sleep(20 * time.Millisecond)

	if calls := upstream.callCount(); calls != 1 {
		t.Fatalf("expected one in-flight upstream refresh, got %d", calls)
	}

	upstream.release()
	upstream.waitDone(t)

	if calls := upstream.callCount(); calls != 1 {
		t.Fatalf("expected one upstream call after dedupe, got %d", calls)
	}
}

func TestRefreshWorkersWithServiceUpdatesCacheMetadataOn304(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 45, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	policy := cache.DefaultPolicy()
	cacheStore := newThreadSafeCacheStore(cache.Record{
		Payload:       []byte(`{"tag_name":"cached"}`),
		ETag:          `"etag-cached"`,
		URL:           rawURL,
		LastCheckedAt: now.Add(-(2 * time.Hour)),
		ExpiresAt:     now.Add(-time.Hour),
		SourceStatus:  http.StatusOK,
	})
	upstream := newBlockingUpstreamClient(http.StatusNotModified, "")
	service := NewService(
		cacheStore,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	metrics := observability.NewMetrics()
	service.SetMetrics(metrics)
	service.now = func() time.Time { return now }

	queue := NewRefreshQueue(2)
	defer queue.Close()
	workers := newRefreshWorkers(queue, service, 1, 1000, nil, time.Now, true)

	if ok := queue.Enqueue(cacheKey); !ok {
		t.Fatal("expected enqueue to succeed")
	}

	upstream.waitStarted(t)
	upstream.release()
	upstream.waitDone(t)
	cacheStore.waitForSetCalls(t, 1, time.Second)
	workers.Close()

	setCalls, lastSet := cacheStore.snapshot()
	if setCalls != 1 {
		t.Fatalf("expected one cache update, got %d", setCalls)
	}
	if string(lastSet.Payload) != `{"tag_name":"cached"}` {
		t.Fatalf("expected cached payload to be preserved, got %s", string(lastSet.Payload))
	}
	if !lastSet.LastCheckedAt.Equal(now) {
		t.Fatalf("expected LastCheckedAt %v, got %v", now, lastSet.LastCheckedAt)
	}
	if !lastSet.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), lastSet.ExpiresAt)
	}

	snapshot := metrics.Snapshot()
	if snapshot.RefreshNotMod != 1 {
		t.Fatalf("expected 1 not-modified refresh, got %d", snapshot.RefreshNotMod)
	}
	if snapshot.RefreshUpdated != 0 {
		t.Fatalf("expected 0 updated refreshes, got %d", snapshot.RefreshUpdated)
	}
	if snapshot.RefreshFailed != 0 {
		t.Fatalf("expected 0 failed refreshes, got %d", snapshot.RefreshFailed)
	}
	if snapshot.RefreshSkipped != 0 {
		t.Fatalf("expected 0 skipped refreshes, got %d", snapshot.RefreshSkipped)
	}
}

func TestRefreshWorkersWithServiceReplacesCacheRecordOn200(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 50, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	policy := cache.DefaultPolicy()
	cacheStore := newThreadSafeCacheStore(cache.Record{
		Payload:       []byte(`{"tag_name":"old"}`),
		ETag:          `"etag-old"`,
		URL:           rawURL,
		LastCheckedAt: now.Add(-(2 * time.Hour)),
		SourceStatus:  http.StatusOK,
	})
	upstream := newBlockingUpstreamClient(http.StatusOK, `{"tag_name":"new"}`)
	service := NewService(
		cacheStore,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	metrics := observability.NewMetrics()
	service.SetMetrics(metrics)
	service.now = func() time.Time { return now }

	queue := NewRefreshQueue(2)
	defer queue.Close()
	workers := newRefreshWorkers(queue, service, 1, 1000, nil, time.Now, true)

	if ok := queue.Enqueue(cacheKey); !ok {
		t.Fatal("expected enqueue to succeed")
	}

	upstream.waitStarted(t)
	upstream.release()
	upstream.waitDone(t)
	cacheStore.waitForSetCalls(t, 1, time.Second)
	workers.Close()

	setCalls, lastSet := cacheStore.snapshot()
	if setCalls != 1 {
		t.Fatalf("expected one cache update, got %d", setCalls)
	}
	if string(lastSet.Payload) != `{"tag_name":"new"}` {
		t.Fatalf("expected payload to be replaced, got %s", string(lastSet.Payload))
	}
	if lastSet.SourceStatus != http.StatusOK {
		t.Fatalf("expected source status %d, got %d", http.StatusOK, lastSet.SourceStatus)
	}
	if !lastSet.FetchedAt.Equal(now) {
		t.Fatalf("expected FetchedAt %v, got %v", now, lastSet.FetchedAt)
	}
	if !lastSet.LastCheckedAt.Equal(now) {
		t.Fatalf("expected LastCheckedAt %v, got %v", now, lastSet.LastCheckedAt)
	}
	if !lastSet.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), lastSet.ExpiresAt)
	}

	snapshot := metrics.Snapshot()
	if snapshot.RefreshUpdated != 1 {
		t.Fatalf("expected 1 updated refresh, got %d", snapshot.RefreshUpdated)
	}
	if snapshot.RefreshNotMod != 0 {
		t.Fatalf("expected 0 not-modified refreshes, got %d", snapshot.RefreshNotMod)
	}
	if snapshot.RefreshFailed != 0 {
		t.Fatalf("expected 0 failed refreshes, got %d", snapshot.RefreshFailed)
	}
	if snapshot.RefreshSkipped != 0 {
		t.Fatalf("expected 0 skipped refreshes, got %d", snapshot.RefreshSkipped)
	}
}

func TestRefreshWorkersEnforcePerWorkerRateLimit(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(4)
	defer queue.Close()

	refresher := newFakeRefreshByKeyRequester()
	workers := newRefreshWorkers(queue, refresher, 1, 2, nil, time.Now, true)
	defer workers.Close()

	if ok := queue.Enqueue("key-1"); !ok {
		t.Fatal("expected first enqueue to succeed")
	}
	if ok := queue.Enqueue("key-2"); !ok {
		t.Fatal("expected second enqueue to succeed")
	}
	if ok := queue.Enqueue("key-3"); !ok {
		t.Fatal("expected third enqueue to succeed")
	}

	refresher.waitForCalls(t, 3, 3*time.Second)

	callTimes := refresher.callTimes()
	if len(callTimes) != 3 {
		t.Fatalf("expected 3 refresh call timestamps, got %d", len(callTimes))
	}

	if gap := callTimes[1].Sub(callTimes[0]); gap < 450*time.Millisecond {
		t.Fatalf("expected >=450ms between first and second calls, got %s", gap)
	}
	if gap := callTimes[2].Sub(callTimes[1]); gap < 450*time.Millisecond {
		t.Fatalf("expected >=450ms between second and third calls, got %s", gap)
	}
}

func TestRefreshWorkersDefaultToOneRequestPerSecond(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(4)
	defer queue.Close()

	refresher := newFakeRefreshByKeyRequester()
	workers := newRefreshWorkers(queue, refresher, 1, 0, nil, time.Now, true)
	defer workers.Close()

	if ok := queue.Enqueue("key-1"); !ok {
		t.Fatal("expected first enqueue to succeed")
	}
	if ok := queue.Enqueue("key-2"); !ok {
		t.Fatal("expected second enqueue to succeed")
	}
	if ok := queue.Enqueue("key-3"); !ok {
		t.Fatal("expected third enqueue to succeed")
	}

	refresher.waitForCalls(t, 3, 5*time.Second)

	callTimes := refresher.callTimes()
	if len(callTimes) != 3 {
		t.Fatalf("expected 3 refresh call timestamps, got %d", len(callTimes))
	}
	if gap := callTimes[1].Sub(callTimes[0]); gap < 850*time.Millisecond {
		t.Fatalf("expected default pacing >=850ms between first and second calls, got %s", gap)
	}
	if gap := callTimes[2].Sub(callTimes[1]); gap < 850*time.Millisecond {
		t.Fatalf("expected default pacing >=850ms between second and third calls, got %s", gap)
	}
}

type fakeRefreshByKeyRequester struct {
	mu    sync.Mutex
	count int
	times []time.Time
	calls chan struct{}
}

type blockingRefreshByKeyRequester struct {
	mu          sync.Mutex
	started     int
	completed   int
	startedCh   chan struct{}
	completedCh chan struct{}
	releaseCh   chan struct{}
	releaseOnce sync.Once
}

func newBlockingRefreshByKeyRequester() *blockingRefreshByKeyRequester {
	return &blockingRefreshByKeyRequester{
		startedCh:   make(chan struct{}, 32),
		completedCh: make(chan struct{}, 32),
		releaseCh:   make(chan struct{}),
	}
}

func (b *blockingRefreshByKeyRequester) RefreshByKey(string) bool {
	b.mu.Lock()
	b.started++
	b.mu.Unlock()

	select {
	case b.startedCh <- struct{}{}:
	default:
	}

	<-b.releaseCh

	b.mu.Lock()
	b.completed++
	b.mu.Unlock()
	select {
	case b.completedCh <- struct{}{}:
	default:
	}

	return true
}

func (b *blockingRefreshByKeyRequester) releaseAll() {
	b.releaseOnce.Do(func() {
		close(b.releaseCh)
	})
}

func (b *blockingRefreshByKeyRequester) waitForStarted(t *testing.T, expected int, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		if b.startedCount() >= expected {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d started refresh calls; got %d", expected, b.startedCount())
		case <-b.startedCh:
		}
	}
}

func (b *blockingRefreshByKeyRequester) waitForCompleted(t *testing.T, expected int, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		if b.completedCount() >= expected {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d completed refresh calls; got %d", expected, b.completedCount())
		case <-b.completedCh:
		}
	}
}

func (b *blockingRefreshByKeyRequester) startedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

func (b *blockingRefreshByKeyRequester) completedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.completed
}

func newFakeRefreshByKeyRequester() *fakeRefreshByKeyRequester {
	return &fakeRefreshByKeyRequester{
		calls: make(chan struct{}, 32),
	}
}

func (f *fakeRefreshByKeyRequester) RefreshByKey(string) bool {
	f.mu.Lock()
	f.count++
	f.times = append(f.times, time.Now())
	f.mu.Unlock()

	select {
	case f.calls <- struct{}{}:
	default:
	}
	return true
}

func (f *fakeRefreshByKeyRequester) waitForCalls(t *testing.T, expected int, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		if f.callCountValue() >= expected {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d refresh calls; got %d", expected, f.callCountValue())
		case <-f.calls:
		}
	}
}

func (f *fakeRefreshByKeyRequester) callCount() int {
	return f.callCountValue()
}

func (f *fakeRefreshByKeyRequester) callCountValue() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (f *fakeRefreshByKeyRequester) callTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]time.Time, len(f.times))
	copy(out, f.times)
	return out
}

type threadSafeCacheStore struct {
	mu       sync.Mutex
	record   cache.Record
	hit      bool
	setCalls int
	lastSet  cache.Record
	setCh    chan struct{}
}

func newThreadSafeCacheStore(record cache.Record) *threadSafeCacheStore {
	return &threadSafeCacheStore{
		record: record,
		hit:    true,
		setCh:  make(chan struct{}, 8),
	}
}

func (s *threadSafeCacheStore) Get(_ context.Context, _ string) (cache.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hit {
		return cache.Record{}, false, nil
	}

	record := s.record
	record.Payload = append([]byte(nil), s.record.Payload...)
	return record, true, nil
}

func (s *threadSafeCacheStore) Set(_ string, record cache.Record) (bool, error) {
	s.mu.Lock()
	s.setCalls++
	s.lastSet = record
	s.lastSet.Payload = append([]byte(nil), record.Payload...)
	s.record = s.lastSet
	s.hit = true
	s.mu.Unlock()

	select {
	case s.setCh <- struct{}{}:
	default:
	}

	return true, nil
}

func (s *threadSafeCacheStore) waitForSetCalls(t *testing.T, expected int, timeout time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		setCalls, _ := s.snapshot()
		if setCalls >= expected {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d cache writes; got %d", expected, setCalls)
		case <-s.setCh:
		}
	}
}

func (s *threadSafeCacheStore) snapshot() (int, cache.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := s.lastSet
	record.Payload = append([]byte(nil), s.lastSet.Payload...)
	return s.setCalls, record
}
