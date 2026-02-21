package l2redis

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"svm/internal/cache"

	"github.com/redis/go-redis/v9"
)

func TestStorePersistAndGetRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := newFakeRedisClient()
	store := NewStoreWithTTL(fake, 10*time.Minute)

	key := "https://api.github.com/repos/org/repo/releases/latest"
	record := &cache.Record{
		Payload:       []byte(`{"tag_name":"v1.2.3"}`),
		ETag:          `"abc123"`,
		FetchedAt:     time.Date(2026, 2, 21, 18, 10, 0, 0, time.UTC),
		LastHitAt:     time.Date(2026, 2, 21, 18, 12, 0, 0, time.UTC),
		LastCheckedAt: time.Date(2026, 2, 21, 18, 12, 30, 0, time.UTC),
		ExpiresAt:     time.Date(2026, 2, 22, 18, 10, 0, 0, time.UTC),
		SourceStatus:  200,
	}

	if err := store.Persist(ctx, key, record); err != nil {
		t.Fatalf("expected nil error persisting record, got %v", err)
	}

	if fake.lastSetKey != namespacedRecordKey(key) {
		t.Fatalf("expected namespaced key %q, got %q", namespacedRecordKey(key), fake.lastSetKey)
	}
	if fake.lastSetTTL != 10*time.Minute {
		t.Fatalf("expected ttl %v, got %v", 10*time.Minute, fake.lastSetTTL)
	}

	got, hit, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("expected nil error hydrating record, got %v", err)
	}
	if !hit {
		t.Fatal("expected hit=true for persisted record")
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("expected record %+v, got %+v", record, got)
	}
}

func TestStoreGetMissReturnsNoRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewStore(newFakeRedisClient())

	record, hit, err := store.Get(ctx, "missing-key")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hit {
		t.Fatal("expected hit=false on cache miss")
	}
	if record != nil {
		t.Fatalf("expected nil record on cache miss, got %+v", record)
	}
}

func TestStoreGetReturnsDecodeErrorForInvalidPayload(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := newFakeRedisClient()
	fake.data[namespacedRecordKey("bad-record")] = []byte("not-json")
	store := NewStore(fake)

	_, _, err := store.Get(ctx, "bad-record")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestStoreReturnsSetError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := newFakeRedisClient()
	fake.setErr = errors.New("redis unavailable")
	store := NewStore(fake)

	err := store.Persist(ctx, "key", &cache.Record{Payload: []byte(`{}`)})
	if err == nil {
		t.Fatal("expected set error, got nil")
	}
}

func TestStoreUpsertActiveKeyAndListSince(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := newFakeRedisClient()
	store := NewStore(fake)

	base := time.Date(2026, 2, 21, 19, 0, 0, 0, time.UTC)
	if err := store.UpsertActiveKey(ctx, "key-old", base); err != nil {
		t.Fatalf("expected nil upsert error, got %v", err)
	}
	if err := store.UpsertActiveKey(ctx, "key-newer", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("expected nil upsert error, got %v", err)
	}
	if err := store.UpsertActiveKey(ctx, "key-newest", base.Add(3*time.Minute)); err != nil {
		t.Fatalf("expected nil upsert error, got %v", err)
	}

	keys, err := store.ActiveKeysSince(ctx, base.Add(time.Minute), 0)
	if err != nil {
		t.Fatalf("expected nil list error, got %v", err)
	}

	want := []string{"key-newer", "key-newest"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("expected active keys %v, got %v", want, keys)
	}
}

func TestStoreActiveKeysSinceHonorsLimit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := newFakeRedisClient()
	store := NewStore(fake)

	base := time.Date(2026, 2, 21, 20, 0, 0, 0, time.UTC)
	_ = store.UpsertActiveKey(ctx, "key-1", base.Add(1*time.Minute))
	_ = store.UpsertActiveKey(ctx, "key-2", base.Add(2*time.Minute))
	_ = store.UpsertActiveKey(ctx, "key-3", base.Add(3*time.Minute))

	keys, err := store.ActiveKeysSince(ctx, base, 2)
	if err != nil {
		t.Fatalf("expected nil list error, got %v", err)
	}
	want := []string{"key-1", "key-2"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("expected limited active keys %v, got %v", want, keys)
	}
}

func TestStoreReturnsZSetErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := newFakeRedisClient()
	store := NewStore(fake)

	fake.zAddErr = errors.New("zadd unavailable")
	if err := store.UpsertActiveKey(ctx, "key", time.Now()); err == nil {
		t.Fatal("expected zadd error, got nil")
	}

	fake.zAddErr = nil
	fake.zRangeErr = errors.New("zrange unavailable")
	if _, err := store.ActiveKeysSince(ctx, time.Now(), 10); err == nil {
		t.Fatal("expected zrange error, got nil")
	}
}

type fakeRedisClient struct {
	data        map[string][]byte
	activeIndex map[string]float64
	lastSetKey  string
	lastSetTTL  time.Duration
	setErr      error
	zAddErr     error
	zRangeErr   error
}

func newFakeRedisClient() *fakeRedisClient {
	return &fakeRedisClient{
		data:        make(map[string][]byte),
		activeIndex: make(map[string]float64),
	}
}

func (f *fakeRedisClient) Get(_ context.Context, key string) *redis.StringCmd {
	if value, ok := f.data[key]; ok {
		return redis.NewStringResult(string(value), nil)
	}

	return redis.NewStringResult("", redis.Nil)
}

func (f *fakeRedisClient) Set(_ context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	f.lastSetKey = key
	f.lastSetTTL = expiration
	if f.setErr != nil {
		return redis.NewStatusResult("", f.setErr)
	}

	switch v := value.(type) {
	case []byte:
		f.data[key] = append([]byte(nil), v...)
	case string:
		f.data[key] = []byte(v)
	default:
		f.data[key] = []byte{}
	}

	return redis.NewStatusResult("OK", nil)
}

func (f *fakeRedisClient) ZAdd(_ context.Context, _ string, members ...redis.Z) *redis.IntCmd {
	if f.zAddErr != nil {
		return redis.NewIntResult(0, f.zAddErr)
	}

	for _, member := range members {
		key, ok := member.Member.(string)
		if !ok || key == "" {
			continue
		}
		f.activeIndex[key] = member.Score
	}

	return redis.NewIntResult(int64(len(members)), nil)
}

func (f *fakeRedisClient) ZRangeByScore(_ context.Context, _ string, opt *redis.ZRangeBy) *redis.StringSliceCmd {
	if f.zRangeErr != nil {
		return redis.NewStringSliceResult(nil, f.zRangeErr)
	}

	min := 0.0
	if opt != nil && opt.Min != "" {
		parsed, err := strconv.ParseFloat(opt.Min, 64)
		if err == nil {
			min = parsed
		}
	}

	type scoredKey struct {
		key   string
		score float64
	}

	candidates := make([]scoredKey, 0, len(f.activeIndex))
	for key, score := range f.activeIndex {
		if score >= min {
			candidates = append(candidates, scoredKey{key: key, score: score})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].score < candidates[j].score
	})

	limit := len(candidates)
	if opt != nil && opt.Count > 0 && int(opt.Count) < limit {
		limit = int(opt.Count)
	}

	keys := make([]string, 0, limit)
	for idx := 0; idx < limit; idx++ {
		keys = append(keys, candidates[idx].key)
	}

	return redis.NewStringSliceResult(keys, nil)
}
