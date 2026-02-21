package l1

import (
	"bytes"
	"testing"
	"time"

	"svm/internal/cache"
)

func TestCacheGetMiss(t *testing.T) {
	t.Parallel()

	l1 := NewCache()

	record, ok := l1.Get("missing")
	if ok {
		t.Fatal("expected cache miss")
	}
	if len(record.Payload) != 0 || record.ETag != "" || !record.FetchedAt.IsZero() ||
		!record.LastHitAt.IsZero() || !record.LastCheckedAt.IsZero() ||
		!record.ExpiresAt.IsZero() || record.SourceStatus != 0 {
		t.Fatalf("expected zero record on miss, got %+v", record)
	}
}

func TestCacheSetGetRoundTripUsesCopies(t *testing.T) {
	t.Parallel()

	l1 := NewCache()
	key := "https://api.github.com/repos/org/repo/releases/latest"
	record := cache.Record{
		Payload:       []byte(`{"tag_name":"v1.2.3"}`),
		ETag:          `"etag-1"`,
		FetchedAt:     time.Date(2026, 2, 21, 20, 0, 0, 0, time.UTC),
		LastHitAt:     time.Date(2026, 2, 21, 20, 1, 0, 0, time.UTC),
		LastCheckedAt: time.Date(2026, 2, 21, 20, 1, 30, 0, time.UTC),
		ExpiresAt:     time.Date(2026, 2, 21, 21, 0, 0, 0, time.UTC),
		SourceStatus:  200,
	}

	l1.Set(key, record)

	record.Payload[0] = '['
	got, ok := l1.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.ETag != record.ETag {
		t.Fatalf("expected etag %q, got %q", record.ETag, got.ETag)
	}
	if !bytes.Equal(got.Payload, []byte(`{"tag_name":"v1.2.3"}`)) {
		t.Fatalf("unexpected payload: %s", string(got.Payload))
	}

	got.Payload[0] = '['
	gotAgain, ok := l1.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !bytes.Equal(gotAgain.Payload, []byte(`{"tag_name":"v1.2.3"}`)) {
		t.Fatalf("expected payload copy to stay unchanged, got %s", string(gotAgain.Payload))
	}
}

func TestCacheEstimatedUsageTracksSetAndOverwrite(t *testing.T) {
	t.Parallel()

	l1 := NewCache()

	l1.Set("a", cache.Record{Payload: []byte("12345")})
	want := recordMetadataOverheadBytes + 5
	if got := l1.EstimatedUsageBytes(); got != want {
		t.Fatalf("expected usage %d, got %d", want, got)
	}
	if got := l1.Len(); got != 1 {
		t.Fatalf("expected len 1, got %d", got)
	}

	l1.Set("b", cache.Record{Payload: []byte("123")})
	want = (2 * recordMetadataOverheadBytes) + 8
	if got := l1.EstimatedUsageBytes(); got != want {
		t.Fatalf("expected usage %d, got %d", want, got)
	}
	if got := l1.Len(); got != 2 {
		t.Fatalf("expected len 2, got %d", got)
	}

	l1.Set("a", cache.Record{Payload: []byte("12")})
	want = (2 * recordMetadataOverheadBytes) + 5
	if got := l1.EstimatedUsageBytes(); got != want {
		t.Fatalf("expected usage %d, got %d", want, got)
	}
	if got := l1.Len(); got != 2 {
		t.Fatalf("expected len 2 after overwrite, got %d", got)
	}
}

func TestCacheEvictsLeastRecentlyUsedWhenBudgetExceeded(t *testing.T) {
	t.Parallel()

	entryBytes := recordMetadataOverheadBytes + 1
	l1 := NewCacheWithMaxBytes(entryBytes * 2)

	l1.Set("a", cache.Record{Payload: []byte("1")})
	l1.Set("b", cache.Record{Payload: []byte("2")})
	if _, ok := l1.Get("a"); !ok {
		t.Fatal("expected key a to exist before eviction")
	}

	l1.Set("c", cache.Record{Payload: []byte("3")})

	if _, ok := l1.Get("b"); ok {
		t.Fatal("expected least recently used key b to be evicted")
	}
	if _, ok := l1.Get("a"); !ok {
		t.Fatal("expected key a to remain after eviction")
	}
	if _, ok := l1.Get("c"); !ok {
		t.Fatal("expected key c to remain after eviction")
	}
	if got := l1.Len(); got != 2 {
		t.Fatalf("expected len 2 after eviction, got %d", got)
	}
	if got := l1.EstimatedUsageBytes(); got > entryBytes*2 {
		t.Fatalf("expected usage <= %d after eviction, got %d", entryBytes*2, got)
	}
}

func TestCacheBudgetDefaultAndOverride(t *testing.T) {
	t.Parallel()

	defaultCache := NewCache()
	defaultCache.Set("a", cache.Record{Payload: []byte("1")})
	if _, ok := defaultCache.Get("a"); !ok {
		t.Fatal("expected default cache budget to keep small entry")
	}

	overrideCache := NewCacheWithMaxGB(0.00000001)
	overrideCache.Set("a", cache.Record{Payload: []byte("1")})
	if _, ok := overrideCache.Get("a"); ok {
		t.Fatal("expected tiny custom budget to evict oversized entry")
	}
}
