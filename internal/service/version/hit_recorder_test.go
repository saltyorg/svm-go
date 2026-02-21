package version

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHitRecorderRecordHitUpsertsInBackground(t *testing.T) {
	t.Parallel()

	index := &fakeActiveKeyIndexer{
		upsertSignals: make(chan upsertCall, 1),
	}
	recorder := NewHitRecorder(index, 4, nil)
	defer recorder.Close()

	hitAt := time.Date(2026, 2, 21, 21, 0, 0, 0, time.UTC)
	if ok := recorder.RecordHit("cache-key-1", hitAt); !ok {
		t.Fatal("expected hit signal to enqueue")
	}

	select {
	case call := <-index.upsertSignals:
		if call.key != "cache-key-1" {
			t.Fatalf("expected key %q, got %q", "cache-key-1", call.key)
		}
		if !call.lastHitAt.Equal(hitAt) {
			t.Fatalf("expected hit time %v, got %v", hitAt, call.lastHitAt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background upsert")
	}
}

func TestHitRecorderDropsWhenQueueIsFull(t *testing.T) {
	t.Parallel()

	blockUpsert := make(chan struct{})
	upsertStarted := make(chan struct{}, 1)
	index := &fakeActiveKeyIndexer{
		blockUpsert:   blockUpsert,
		upsertStarted: upsertStarted,
	}
	recorder := NewHitRecorder(index, 1, nil)
	defer recorder.Close()

	if ok := recorder.RecordHit("cache-key-1", time.Now()); !ok {
		t.Fatal("expected first hit signal to enqueue")
	}

	select {
	case <-upsertStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first upsert to start")
	}

	if ok := recorder.RecordHit("cache-key-2", time.Now()); !ok {
		t.Fatal("expected second hit signal to fill queue")
	}
	if ok := recorder.RecordHit("cache-key-3", time.Now()); ok {
		t.Fatal("expected third hit signal to drop when queue is full")
	}

	close(blockUpsert)
}

func TestHitRecorderActiveKeysSinceDelegates(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 2, 14, 0, 0, 0, 0, time.UTC)
	index := &fakeActiveKeyIndexer{
		activeKeysResult: []string{"key-1", "key-2"},
	}
	recorder := NewHitRecorder(index, 2, nil)
	defer recorder.Close()

	got, err := recorder.ActiveKeysSince(context.Background(), since, 10)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !reflect.DeepEqual(got, index.activeKeysResult) {
		t.Fatalf("expected active keys %v, got %v", index.activeKeysResult, got)
	}
	if !index.lastSince.Equal(since) {
		t.Fatalf("expected since %v, got %v", since, index.lastSince)
	}
	if index.lastLimit != 10 {
		t.Fatalf("expected limit %d, got %d", 10, index.lastLimit)
	}
}

func TestHitRecorderHandlesNilIndexer(t *testing.T) {
	t.Parallel()

	recorder := NewHitRecorder(nil, 1, nil)
	defer recorder.Close()

	if ok := recorder.RecordHit("cache-key", time.Now()); ok {
		t.Fatal("expected enqueue failure for nil indexer")
	}

	keys, err := recorder.ActiveKeysSince(context.Background(), time.Now(), 1)
	if err != nil {
		t.Fatalf("expected nil error for nil indexer, got %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil key list for nil indexer, got %v", keys)
	}
}

type upsertCall struct {
	key       string
	lastHitAt time.Time
}

type fakeActiveKeyIndexer struct {
	upsertSignals chan upsertCall
	upsertErr     error
	blockUpsert   chan struct{}
	upsertStarted chan struct{}

	activeKeysResult []string
	activeKeysErr    error
	lastSince        time.Time
	lastLimit        int
}

func (f *fakeActiveKeyIndexer) UpsertActiveKey(
	_ context.Context,
	key string,
	lastHitAt time.Time,
) error {
	if f.upsertStarted != nil {
		select {
		case f.upsertStarted <- struct{}{}:
		default:
		}
	}
	if f.blockUpsert != nil {
		<-f.blockUpsert
	}
	if f.upsertSignals != nil {
		f.upsertSignals <- upsertCall{
			key:       key,
			lastHitAt: lastHitAt,
		}
	}
	if f.upsertErr != nil {
		return f.upsertErr
	}

	return nil
}

func (f *fakeActiveKeyIndexer) ActiveKeysSince(
	_ context.Context,
	since time.Time,
	limit int,
) ([]string, error) {
	f.lastSince = since
	f.lastLimit = limit
	if f.activeKeysErr != nil {
		return nil, f.activeKeysErr
	}
	return append([]string(nil), f.activeKeysResult...), nil
}

func TestHitRecorderContinuesAfterUpsertError(t *testing.T) {
	t.Parallel()

	index := &fakeActiveKeyIndexer{
		upsertSignals: make(chan upsertCall, 2),
		upsertErr:     errors.New("redis unavailable"),
	}
	recorder := NewHitRecorder(index, 2, nil)
	defer recorder.Close()

	if ok := recorder.RecordHit("cache-key-1", time.Now()); !ok {
		t.Fatal("expected first enqueue success")
	}
	if ok := recorder.RecordHit("cache-key-2", time.Now()); !ok {
		t.Fatal("expected second enqueue success")
	}

	select {
	case <-index.upsertSignals:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first upsert attempt")
	}
	select {
	case <-index.upsertSignals:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second upsert attempt")
	}
}
