package version

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/cache/l1"
)

func TestWriteBehindEnqueueCoalescesLatestByKey(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	writer := newWriteBehind(
		&fakeCacheRecordPersister{},
		4,
		time.Hour,
		30*time.Second,
		5*time.Minute,
		nil,
		func() time.Time { return now },
		false,
	)

	first := cache.Record{Payload: []byte(`{"tag_name":"v1"}`), ETag: `"etag-1"`}
	second := cache.Record{Payload: []byte(`{"tag_name":"v2"}`), ETag: `"etag-2"`}
	if ok := writer.Enqueue("key-1", first); !ok {
		t.Fatal("expected first enqueue to succeed")
	}
	if ok := writer.Enqueue("key-1", second); !ok {
		t.Fatal("expected coalesced enqueue to succeed")
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()

	if writer.order.Len() != 1 {
		t.Fatalf("expected one pending entry, got %d", writer.order.Len())
	}
	entry := writer.order.Front().Value.(*pendingWrite)
	if entry.key != "key-1" {
		t.Fatalf("expected key %q, got %q", "key-1", entry.key)
	}
	if !reflect.DeepEqual(entry.record, second) {
		t.Fatalf("expected coalesced record %+v, got %+v", second, entry.record)
	}
}

func TestWriteBehindEnqueueDropsOldestWhenQueueFull(t *testing.T) {
	t.Parallel()

	writer := newWriteBehind(
		&fakeCacheRecordPersister{},
		2,
		time.Hour,
		30*time.Second,
		5*time.Minute,
		nil,
		time.Now,
		false,
	)

	if ok := writer.Enqueue("key-1", cache.Record{Payload: []byte(`{"id":1}`)}); !ok {
		t.Fatal("expected enqueue key-1 to succeed")
	}
	if ok := writer.Enqueue("key-2", cache.Record{Payload: []byte(`{"id":2}`)}); !ok {
		t.Fatal("expected enqueue key-2 to succeed")
	}
	if ok := writer.Enqueue("key-3", cache.Record{Payload: []byte(`{"id":3}`)}); !ok {
		t.Fatal("expected enqueue key-3 to succeed with overflow drop")
	}

	got := pendingKeysInOrder(writer)
	want := []string{"key-2", "key-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected pending keys %v, got %v", want, got)
	}
}

func TestWriteBehindFlushRetriesWithExponentialBackoff(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	persister := &fakeCacheRecordPersister{
		errSequence: []error{
			errors.New("redis unavailable"),
			errors.New("redis unavailable"),
			nil,
		},
	}
	writer := newWriteBehind(
		persister,
		4,
		time.Second,
		8*time.Second,
		time.Minute,
		nil,
		func() time.Time { return now },
		false,
	)

	if ok := writer.Enqueue("key-1", cache.Record{Payload: []byte(`{"tag_name":"v1"}`)}); !ok {
		t.Fatal("expected enqueue success")
	}

	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 1 {
		t.Fatalf("expected one persist attempt after first flush, got %d", calls)
	}
	entry := pendingEntry(writer, "key-1")
	if entry == nil {
		t.Fatal("expected entry to be requeued after first failure")
	}
	if got := entry.nextAttemptAt.Sub(now); got != time.Second {
		t.Fatalf("expected first retry delay 1s, got %s", got)
	}

	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 1 {
		t.Fatalf("expected no retry before backoff elapses, got %d calls", calls)
	}

	now = now.Add(time.Second)
	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 2 {
		t.Fatalf("expected second persist attempt, got %d", calls)
	}
	entry = pendingEntry(writer, "key-1")
	if entry == nil {
		t.Fatal("expected entry to be requeued after second failure")
	}
	if got := entry.nextAttemptAt.Sub(now); got != 2*time.Second {
		t.Fatalf("expected second retry delay 2s, got %s", got)
	}

	now = now.Add(2 * time.Second)
	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 3 {
		t.Fatalf("expected third persist attempt, got %d", calls)
	}
	if entry = pendingEntry(writer, "key-1"); entry != nil {
		t.Fatal("expected entry to be removed after successful persist")
	}
}

func TestWriteBehindFlushDropsExpiredRetryEvent(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	persister := &fakeCacheRecordPersister{
		errSequence: []error{errors.New("redis unavailable")},
	}
	writer := newWriteBehind(
		persister,
		4,
		time.Second,
		8*time.Second,
		1500*time.Millisecond,
		nil,
		func() time.Time { return now },
		false,
	)

	if ok := writer.Enqueue("key-1", cache.Record{Payload: []byte(`{"tag_name":"v1"}`)}); !ok {
		t.Fatal("expected enqueue success")
	}

	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 1 {
		t.Fatalf("expected initial persist attempt, got %d", calls)
	}
	if pendingEntry(writer, "key-1") == nil {
		t.Fatal("expected entry to remain pending after first failure")
	}

	now = now.Add(2 * time.Second)
	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 1 {
		t.Fatalf("expected stale entry to drop without new persist call, got %d", calls)
	}
	if pendingEntry(writer, "key-1") != nil {
		t.Fatal("expected stale entry to be dropped")
	}
}

func TestWriteBehindFailureDoesNotBreakFacadeSetAndEventuallyPersists(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	persister := &fakeCacheRecordPersister{
		errSequence: []error{
			errors.New("redis unavailable"),
			nil,
		},
	}
	writer := newWriteBehind(
		persister,
		4,
		time.Second,
		8*time.Second,
		5*time.Minute,
		nil,
		func() time.Time { return now },
		false,
	)

	l1Store := l1.NewCacheWithMaxBytes(1 << 20)
	facade := cache.NewFacade(l1Store, nil, writer)
	record := cache.Record{Payload: []byte(`{"tag_name":"v5.0.0"}`), SourceStatus: 200}

	start := time.Now()
	enqueued, err := facade.Set("key-1", record)
	if err != nil {
		t.Fatalf("expected nil error from facade set, got %v", err)
	}
	if !enqueued {
		t.Fatal("expected write-behind enqueue to succeed")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("expected non-blocking request path, facade set took %s", elapsed)
	}

	l1Record, hit := l1Store.Get("key-1")
	if !hit {
		t.Fatal("expected L1 write to succeed even when persistence later fails")
	}
	if !reflect.DeepEqual(l1Record, record) {
		t.Fatalf("expected L1 record %+v, got %+v", record, l1Record)
	}

	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 1 {
		t.Fatalf("expected one persistence attempt, got %d", calls)
	}
	if pendingEntry(writer, "key-1") == nil {
		t.Fatal("expected failed write to stay queued for retry")
	}

	now = now.Add(time.Second)
	writer.flushOnce(context.Background())
	if calls := persister.callCount(); calls != 2 {
		t.Fatalf("expected second persistence attempt after recovery, got %d", calls)
	}
	if pendingEntry(writer, "key-1") != nil {
		t.Fatal("expected queued write to clear after recovery")
	}
}

func TestWriteBehindCloseWithDrainFlushesPendingEvents(t *testing.T) {
	t.Parallel()

	persister := &fakeCacheRecordPersister{}
	writer := newWriteBehind(
		persister,
		4,
		time.Hour,
		30*time.Second,
		5*time.Minute,
		nil,
		time.Now,
		false,
	)

	if ok := writer.Enqueue("key-1", cache.Record{Payload: []byte(`{"id":1}`)}); !ok {
		t.Fatal("expected enqueue key-1 to succeed")
	}
	if ok := writer.Enqueue("key-2", cache.Record{Payload: []byte(`{"id":2}`)}); !ok {
		t.Fatal("expected enqueue key-2 to succeed")
	}

	writer.CloseWithDrain(200 * time.Millisecond)

	if calls := persister.callCount(); calls != 2 {
		t.Fatalf("expected 2 persisted writes during shutdown drain, got %d", calls)
	}
	if got := pendingKeysInOrder(writer); len(got) != 0 {
		t.Fatalf("expected pending queue to be empty after drain, got %v", got)
	}
	if ok := writer.Enqueue("key-3", cache.Record{Payload: []byte(`{"id":3}`)}); ok {
		t.Fatal("expected enqueue to fail after close")
	}
}

func TestWriteBehindCloseWithDrainRespectsTimeout(t *testing.T) {
	t.Parallel()

	persister := &fakeCacheRecordPersister{
		blockUntilContextDone: true,
	}
	writer := newWriteBehind(
		persister,
		4,
		time.Hour,
		30*time.Second,
		5*time.Minute,
		nil,
		time.Now,
		false,
	)

	if ok := writer.Enqueue("key-1", cache.Record{Payload: []byte(`{"id":1}`)}); !ok {
		t.Fatal("expected enqueue success")
	}

	start := time.Now()
	writer.CloseWithDrain(40 * time.Millisecond)
	elapsed := time.Since(start)

	if calls := persister.callCount(); calls != 1 {
		t.Fatalf("expected one drain persistence attempt, got %d", calls)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("expected close with drain to return within timeout budget, took %s", elapsed)
	}
}

func pendingKeysInOrder(writer *WriteBehind) []string {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	keys := make([]string, 0, writer.order.Len())
	for node := writer.order.Front(); node != nil; node = node.Next() {
		keys = append(keys, node.Value.(*pendingWrite).key)
	}
	return keys
}

func pendingEntry(writer *WriteBehind, key string) *pendingWrite {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	entry, ok := writer.pending[key]
	if !ok {
		return nil
	}
	cloned := *entry
	return &cloned
}

type fakeCacheRecordPersister struct {
	mu                    sync.Mutex
	calls                 int
	errSequence           []error
	blockUntilContextDone bool
}

func (f *fakeCacheRecordPersister) Persist(ctx context.Context, _ string, _ *cache.Record) error {
	f.mu.Lock()
	f.calls++
	calls := f.calls
	f.mu.Unlock()

	if f.blockUntilContextDone {
		<-ctx.Done()
		return ctx.Err()
	}
	if calls <= len(f.errSequence) {
		return f.errSequence[calls-1]
	}
	return nil
}

func (f *fakeCacheRecordPersister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
