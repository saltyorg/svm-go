package version

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/cache/l1"
	"svm/internal/observability"
	upstreamgithub "svm/internal/upstream/github"
)

func TestServiceHandleFreshCacheHitEmitsAsyncHitSignal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 30, 0, 0, time.UTC)
	payload := []byte(`{"tag_name":"v1.2.3"}`)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       payload,
			ETag:          `"etag-1"`,
			LastCheckedAt: now,
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{}
	hitRecorder := &fakeHitSignalRecorder{enqueueResult: true}
	service := NewService(
		cacheStore,
		hitRecorder,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if upstream.getCalls != 0 {
		t.Fatalf("expected no upstream calls on fresh hit, got %d", upstream.getCalls)
	}
	if cacheStore.setCalls != 0 {
		t.Fatalf("expected no cache writes on fresh hit, got %d", cacheStore.setCalls)
	}
	if hitRecorder.calls != 1 {
		t.Fatalf("expected one hit signal, got %d", hitRecorder.calls)
	}

	expectedKey, err := cache.KeyFromURL("https://api.github.com/repos/org/repo/releases/latest")
	if err != nil {
		t.Fatalf("failed to build expected key: %v", err)
	}
	if hitRecorder.lastKey != expectedKey {
		t.Fatalf("unexpected hit signal key: %q", hitRecorder.lastKey)
	}
	if !hitRecorder.lastAt.Equal(now) {
		t.Fatalf("expected hit timestamp %v, got %v", now, hitRecorder.lastAt)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != string(payload) {
		t.Fatalf("expected payload %s, got %s", string(payload), string(rawBody))
	}
}

func TestServiceHandleFreshCacheHitSkipsTokenSelection(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 31, 0, 0, time.UTC)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       []byte(`{"tag_name":"cached"}`),
			LastCheckedAt: now,
			SourceStatus:  http.StatusOK,
		},
		hit: true,
	}
	tokenProvider := &countingTokenProvider{token: "token-a"}
	service := NewService(
		cacheStore,
		nil,
		tokenProvider,
		&fakeUpstreamClient{},
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if tokenProvider.calls != 0 {
		t.Fatalf("expected no token selection on fresh cache hit, got %d calls", tokenProvider.calls)
	}
}

func TestServiceHandleMissSelectsTokenForUpstreamRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 32, 0, 0, time.UTC)
	tokenProvider := &countingTokenProvider{token: "token-a"}
	upstream := &fakeUpstreamClient{
		responseStatus: http.StatusOK,
		responseBody:   `{"tag_name":"upstream"}`,
	}
	service := NewService(
		&fakeCacheStore{},
		nil,
		tokenProvider,
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if tokenProvider.calls != 1 {
		t.Fatalf("expected one token selection on cache miss, got %d", tokenProvider.calls)
	}
}

func TestServiceHandleMissMarksNewCacheEntryAsActive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 32, 30, 0, time.UTC)
	tokenProvider := &countingTokenProvider{token: "token-a"}
	upstream := &fakeUpstreamClient{
		responseStatus: http.StatusOK,
		responseBody:   `{"tag_name":"upstream"}`,
	}
	hitRecorder := &fakeHitSignalRecorder{enqueueResult: true}
	service := NewService(
		&fakeCacheStore{},
		hitRecorder,
		tokenProvider,
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if hitRecorder.calls != 1 {
		t.Fatalf("expected one hit signal on cache insert, got %d", hitRecorder.calls)
	}

	expectedKey, err := cache.KeyFromURL("https://api.github.com/repos/org/repo/releases/latest")
	if err != nil {
		t.Fatalf("failed to build expected key: %v", err)
	}
	if hitRecorder.lastKey != expectedKey {
		t.Fatalf("unexpected hit signal key: %q", hitRecorder.lastKey)
	}
	if !hitRecorder.lastAt.Equal(now) {
		t.Fatalf("expected hit timestamp %v, got %v", now, hitRecorder.lastAt)
	}
}

func TestServiceHandleMetricsCountHitMissAndUpstreamRequests(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 32, 0, 0, time.UTC)
	metrics := observability.NewMetrics()

	hitService := NewService(
		&fakeCacheStore{
			record: cache.Record{
				Payload:       []byte(`{"tag_name":"cached"}`),
				LastCheckedAt: now,
			},
			hit: true,
		},
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		&fakeUpstreamClient{},
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	hitService.SetMetrics(metrics)
	hitService.now = func() time.Time { return now }
	_ = hitService.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	missUpstream := &fakeUpstreamClient{
		responseStatus: http.StatusOK,
		responseBody:   `{"tag_name":"miss-refresh"}`,
	}
	missService := NewService(
		&fakeCacheStore{},
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		missUpstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	missService.SetMetrics(metrics)
	missService.now = func() time.Time { return now }
	_ = missService.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	snapshot := metrics.Snapshot()
	if snapshot.CacheHits != 1 {
		t.Fatalf("expected 1 cache hit, got %d", snapshot.CacheHits)
	}
	if snapshot.CacheMisses != 1 {
		t.Fatalf("expected 1 cache miss, got %d", snapshot.CacheMisses)
	}
	if snapshot.UpstreamRequests != 1 {
		t.Fatalf("expected 1 upstream request, got %d", snapshot.UpstreamRequests)
	}
	if snapshot.UpstreamErrors != 0 {
		t.Fatalf("expected 0 upstream errors, got %d", snapshot.UpstreamErrors)
	}
}

func TestServiceHandleMetricsCountUpstreamErrors(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 33, 0, 0, time.UTC)
	metrics := observability.NewMetrics()
	upstream := &fakeUpstreamClient{
		responseStatus: http.StatusServiceUnavailable,
		responseBody:   `{"error":"unavailable"}`,
	}
	service := NewService(
		&fakeCacheStore{},
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.SetMetrics(metrics)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, response.StatusCode)
	}

	snapshot := metrics.Snapshot()
	if snapshot.CacheMisses != 1 {
		t.Fatalf("expected 1 cache miss, got %d", snapshot.CacheMisses)
	}
	if snapshot.UpstreamRequests != 1 {
		t.Fatalf("expected 1 upstream request, got %d", snapshot.UpstreamRequests)
	}
	if snapshot.UpstreamErrors != 1 {
		t.Fatalf("expected 1 upstream error, got %d", snapshot.UpstreamErrors)
	}
}

func TestServiceHandleFreshCacheHitReturnsWhenHitSignalDrops(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 35, 0, 0, time.UTC)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       []byte(`{"tag_name":"v9.9.9"}`),
			ETag:          `"etag-2"`,
			LastCheckedAt: now,
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{}
	hitRecorder := &fakeHitSignalRecorder{enqueueResult: false}
	service := NewService(
		cacheStore,
		hitRecorder,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if upstream.getCalls != 0 {
		t.Fatalf("expected no upstream calls on fresh hit, got %d", upstream.getCalls)
	}
	if hitRecorder.calls != 1 {
		t.Fatalf("expected one hit signal attempt, got %d", hitRecorder.calls)
	}
}

func TestServiceHandleRefreshOverrideBypassesFreshCacheHit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 21, 40, 0, 0, time.UTC)
	payload := []byte(`{"tag_name":"cached"}`)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       payload,
			ETag:          `"etag-cached"`,
			LastCheckedAt: now,
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{responseStatus: http.StatusNotModified}
	hitRecorder := &fakeHitSignalRecorder{enqueueResult: true}
	policy := cache.DefaultPolicy()
	service := NewService(
		cacheStore,
		hitRecorder,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL:       "https://api.github.com/repos/org/repo/releases/latest",
		ForceRefresh: true,
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if response.CacheStatus != CacheStatusRevalidated {
		t.Fatalf("expected cache status %q, got %q", CacheStatusRevalidated, response.CacheStatus)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call when refresh override is set, got %d", upstream.getCalls)
	}
	if upstream.lastHeaders["If-None-Match"] != `"etag-cached"` {
		t.Fatalf("expected If-None-Match %q, got %q", `"etag-cached"`, upstream.lastHeaders["If-None-Match"])
	}
	if cacheStore.setCalls != 1 {
		t.Fatalf("expected one cache write after upstream revalidation, got %d", cacheStore.setCalls)
	}
	if !cacheStore.lastSet.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), cacheStore.lastSet.ExpiresAt)
	}
	if hitRecorder.calls != 1 {
		t.Fatalf("expected one hit signal on 304 revalidation, got %d", hitRecorder.calls)
	}
}

func TestServiceHandleHydratesFromL2MissWithoutUpstreamCall(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 0, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	l1Store := l1.NewCacheWithMaxBytes(1 << 20)
	hydratedRecord := cache.Record{
		Payload:       []byte(`{"tag_name":"v2.4.6"}`),
		LastCheckedAt: now,
	}
	l2Store := &fakeHydrationL2Store{
		records: map[string]cache.Record{
			cacheKey: hydratedRecord,
		},
	}
	facade := cache.NewFacade(l1Store, l2Store, nil)
	upstream := &fakeUpstreamClient{}
	service := NewService(
		facade,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{RawURL: rawURL})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if upstream.getCalls != 0 {
		t.Fatalf("expected no upstream calls on hydrate hit, got %d", upstream.getCalls)
	}
	if l2Store.getCalls != 1 {
		t.Fatalf("expected one L2 hydration read, got %d", l2Store.getCalls)
	}
	backfilledRecord, hit := l1Store.Get(cacheKey)
	if !hit {
		t.Fatal("expected L2 hydrate hit to backfill L1")
	}
	if string(backfilledRecord.Payload) != string(hydratedRecord.Payload) {
		t.Fatalf("expected L1 payload %s, got %s", string(hydratedRecord.Payload), string(backfilledRecord.Payload))
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != string(hydratedRecord.Payload) {
		t.Fatalf("expected payload %s, got %s", string(hydratedRecord.Payload), string(rawBody))
	}
}

func TestServiceHandleMissPerformsSynchronousUpstreamRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 5, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	l1Store := l1.NewCacheWithMaxBytes(1 << 20)
	l2Store := &fakeHydrationL2Store{records: map[string]cache.Record{}}
	facade := cache.NewFacade(l1Store, l2Store, nil)
	upstream := &fakeUpstreamClient{}
	policy := cache.DefaultPolicy()
	service := NewService(
		facade,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{RawURL: rawURL})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if l2Store.getCalls != 1 {
		t.Fatalf("expected one L2 read before upstream refresh, got %d", l2Store.getCalls)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call on cache miss, got %d", upstream.getCalls)
	}
	record, hit := l1Store.Get(cacheKey)
	if !hit {
		t.Fatal("expected refreshed record stored in L1")
	}
	if string(record.Payload) != `{"tag_name":"upstream"}` {
		t.Fatalf("expected L1 payload %s, got %s", `{"tag_name":"upstream"}`, string(record.Payload))
	}
	if !record.LastCheckedAt.Equal(now) {
		t.Fatalf("expected LastCheckedAt %v, got %v", now, record.LastCheckedAt)
	}
	if !record.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), record.ExpiresAt)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != `{"tag_name":"upstream"}` {
		t.Fatalf("expected response payload %s, got %s", `{"tag_name":"upstream"}`, string(rawBody))
	}
}

func TestServiceHandleMissReturnsWhenWriteBehindDrops(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 7, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	l1Store := l1.NewCacheWithMaxBytes(1 << 20)
	writeBehind := &fakeDroppingWriteBehind{}
	facade := cache.NewFacade(l1Store, nil, writeBehind)
	upstream := &fakeUpstreamClient{
		responseStatus: http.StatusOK,
		responseBody:   `{"tag_name":"write-behind-drop"}`,
	}
	service := NewService(
		facade,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	start := time.Now()
	response := service.Handle(context.Background(), Request{RawURL: rawURL})
	elapsed := time.Since(start)

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if response.CacheStatus != CacheStatusMiss {
		t.Fatalf("expected cache status %q, got %q", CacheStatusMiss, response.CacheStatus)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call, got %d", upstream.getCalls)
	}
	if writeBehind.enqueueCalls != 1 {
		t.Fatalf("expected one write-behind enqueue attempt, got %d", writeBehind.enqueueCalls)
	}
	if writeBehind.lastKey != cacheKey {
		t.Fatalf("expected write-behind key %q, got %q", cacheKey, writeBehind.lastKey)
	}
	if string(writeBehind.lastRecord.Payload) != `{"tag_name":"write-behind-drop"}` {
		t.Fatalf(
			"expected write-behind payload %s, got %s",
			`{"tag_name":"write-behind-drop"}`,
			string(writeBehind.lastRecord.Payload),
		)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected write-behind drop to be non-blocking, request took %s", elapsed)
	}

	record, hit := l1Store.Get(cacheKey)
	if !hit {
		t.Fatal("expected refreshed record stored in L1 even when write-behind drops")
	}
	if string(record.Payload) != `{"tag_name":"write-behind-drop"}` {
		t.Fatalf(
			"expected L1 payload %s, got %s",
			`{"tag_name":"write-behind-drop"}`,
			string(record.Payload),
		)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != `{"tag_name":"write-behind-drop"}` {
		t.Fatalf(
			"expected response payload %s, got %s",
			`{"tag_name":"write-behind-drop"}`,
			string(rawBody),
		)
	}
}

func TestServiceHandleStaleCacheHitReturnsImmediatelyWithoutRevalidation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 10, 0, 0, time.UTC)
	payload := []byte(`{"tag_name":"cached"}`)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       payload,
			ETag:          `"etag-cached"`,
			LastCheckedAt: now.Add(-(2 * time.Hour)),
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{responseStatus: http.StatusNotModified}
	hitRecorder := &fakeHitSignalRecorder{enqueueResult: true}
	service := NewService(
		cacheStore,
		hitRecorder,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if response.CacheStatus != CacheStatusHit {
		t.Fatalf("expected cache status %q, got %q", CacheStatusHit, response.CacheStatus)
	}
	if upstream.getCalls != 0 {
		t.Fatalf("expected no upstream calls on stale cache hit, got %d", upstream.getCalls)
	}
	if cacheStore.setCalls != 0 {
		t.Fatalf("expected no cache writes on stale cache hit, got %d", cacheStore.setCalls)
	}
	if hitRecorder.calls != 1 {
		t.Fatalf("expected one hit signal on stale hit response, got %d", hitRecorder.calls)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != string(payload) {
		t.Fatalf("expected payload %s, got %s", string(payload), string(rawBody))
	}
}

func TestServiceHandleHardExpiredPositiveCacheFallsBackToSynchronousUpstreamFetch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 12, 0, 0, time.UTC)
	oldPayload := []byte(`{"tag_name":"cached-old"}`)
	newPayload := `{"tag_name":"upstream-fresh"}`
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       oldPayload,
			ETag:          `"etag-old"`,
			FetchedAt:     now.Add(-48 * time.Hour),
			LastCheckedAt: now.Add(-48 * time.Hour),
			ExpiresAt:     now.Add(-time.Second),
			SourceStatus:  http.StatusOK,
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{
		responseStatus:  http.StatusOK,
		responseBody:    newPayload,
		responseHeaders: http.Header{"ETag": []string{`"etag-new"`}},
	}
	policy := cache.DefaultPolicy()
	service := NewService(
		cacheStore,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if response.CacheStatus != CacheStatusMiss {
		t.Fatalf("expected cache status %q, got %q", CacheStatusMiss, response.CacheStatus)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call after hard-expired cache hit, got %d", upstream.getCalls)
	}
	if upstream.lastHeaders["If-None-Match"] != `"etag-old"` {
		t.Fatalf("expected If-None-Match %q for hard-expired entry, got %q", `"etag-old"`, upstream.lastHeaders["If-None-Match"])
	}
	if cacheStore.setCalls != 1 {
		t.Fatalf("expected one cache write with refreshed upstream data, got %d", cacheStore.setCalls)
	}
	if string(cacheStore.lastSet.Payload) != newPayload {
		t.Fatalf("expected payload %s, got %s", newPayload, string(cacheStore.lastSet.Payload))
	}
	if !cacheStore.lastSet.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), cacheStore.lastSet.ExpiresAt)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != newPayload {
		t.Fatalf("expected payload %s, got %s", newPayload, string(rawBody))
	}
}

func TestServiceHandleHardExpiredPositiveCacheRevalidatesWith304UsingCachedPayload(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 13, 0, 0, time.UTC)
	oldPayload := []byte(`{"tag_name":"cached-old"}`)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       oldPayload,
			ETag:          `"etag-old"`,
			FetchedAt:     now.Add(-48 * time.Hour),
			LastCheckedAt: now.Add(-48 * time.Hour),
			ExpiresAt:     now.Add(-time.Second),
			SourceStatus:  http.StatusOK,
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{responseStatus: http.StatusNotModified}
	hitRecorder := &fakeHitSignalRecorder{enqueueResult: true}
	policy := cache.DefaultPolicy()
	service := NewService(
		cacheStore,
		hitRecorder,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if response.CacheStatus != CacheStatusRevalidated {
		t.Fatalf("expected cache status %q, got %q", CacheStatusRevalidated, response.CacheStatus)
	}
	if response.UpstreamStatus != http.StatusNotModified {
		t.Fatalf("expected upstream status %d, got %d", http.StatusNotModified, response.UpstreamStatus)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call after hard-expired cache hit, got %d", upstream.getCalls)
	}
	if upstream.lastHeaders["If-None-Match"] != `"etag-old"` {
		t.Fatalf("expected If-None-Match %q for hard-expired entry, got %q", `"etag-old"`, upstream.lastHeaders["If-None-Match"])
	}
	if cacheStore.setCalls != 1 {
		t.Fatalf("expected one cache write after upstream 304, got %d", cacheStore.setCalls)
	}
	if !cacheStore.lastSet.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), cacheStore.lastSet.ExpiresAt)
	}
	if hitRecorder.calls != 1 {
		t.Fatalf("expected one hit signal on 304 revalidation, got %d", hitRecorder.calls)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != string(oldPayload) {
		t.Fatalf("expected payload %s, got %s", string(oldPayload), string(rawBody))
	}
}

func TestServiceHandleRefreshOverrideOnStaleHitReplacesPayloadAndETagOn200(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 15, 0, 0, time.UTC)
	oldPayload := []byte(`{"tag_name":"cached-old"}`)
	newPayload := `{"tag_name":"upstream-new"}`
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       oldPayload,
			ETag:          `"etag-old"`,
			LastCheckedAt: now.Add(-(2 * time.Hour)),
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{
		responseStatus:  http.StatusOK,
		responseBody:    newPayload,
		responseHeaders: http.Header{"ETag": []string{`"etag-new"`}},
	}
	policy := cache.DefaultPolicy()
	service := NewService(
		cacheStore,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	service.now = func() time.Time { return now }

	response := service.Handle(context.Background(), Request{
		RawURL:       "https://api.github.com/repos/org/repo/releases/latest",
		ForceRefresh: true,
	})

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call on stale hit, got %d", upstream.getCalls)
	}
	if upstream.lastHeaders["If-None-Match"] != `"etag-old"` {
		t.Fatalf("expected If-None-Match %q, got %q", `"etag-old"`, upstream.lastHeaders["If-None-Match"])
	}
	if cacheStore.setCalls != 1 {
		t.Fatalf("expected one cache write after upstream 200, got %d", cacheStore.setCalls)
	}
	if string(cacheStore.lastSet.Payload) != newPayload {
		t.Fatalf("expected refreshed payload %s, got %s", newPayload, string(cacheStore.lastSet.Payload))
	}
	if cacheStore.lastSet.ETag != `"etag-new"` {
		t.Fatalf("expected refreshed etag %q, got %q", `"etag-new"`, cacheStore.lastSet.ETag)
	}
	if !cacheStore.lastSet.LastCheckedAt.Equal(now) {
		t.Fatalf("expected LastCheckedAt %v, got %v", now, cacheStore.lastSet.LastCheckedAt)
	}
	if !cacheStore.lastSet.LastHitAt.Equal(now) {
		t.Fatalf("expected LastHitAt %v, got %v", now, cacheStore.lastSet.LastHitAt)
	}
	if !cacheStore.lastSet.FetchedAt.Equal(now) {
		t.Fatalf("expected FetchedAt %v, got %v", now, cacheStore.lastSet.FetchedAt)
	}
	if !cacheStore.lastSet.ExpiresAt.Equal(now.Add(policy.HardTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.HardTTL), cacheStore.lastSet.ExpiresAt)
	}

	rawBody, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected response body type %T, got %T", json.RawMessage{}, response.Body)
	}
	if string(rawBody) != newPayload {
		t.Fatalf("expected payload %s, got %s", newPayload, string(rawBody))
	}
}

func TestServiceHandleCachesNegativeResponseAndServesWithinNegativeTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 20, 0, 0, time.UTC)
	cacheStore := &fakeCacheStore{}
	upstream := &fakeUpstreamClient{
		responseStatus: http.StatusNotFound,
		responseBody:   `{"reason":"missing"}`,
	}
	policy := cache.DefaultPolicy()
	policy.NegativeTTL = 3 * time.Minute
	service := NewService(
		cacheStore,
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		policy,
	)
	service.now = func() time.Time { return now }

	firstResponse := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})
	if firstResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, firstResponse.StatusCode)
	}
	if cacheStore.setCalls != 1 {
		t.Fatalf("expected one cache write for negative response, got %d", cacheStore.setCalls)
	}
	if cacheStore.lastSet.SourceStatus != http.StatusNotFound {
		t.Fatalf("expected cached source status %d, got %d", http.StatusNotFound, cacheStore.lastSet.SourceStatus)
	}
	if !cacheStore.lastSet.ExpiresAt.Equal(now.Add(policy.NegativeTTL)) {
		t.Fatalf("expected ExpiresAt %v, got %v", now.Add(policy.NegativeTTL), cacheStore.lastSet.ExpiresAt)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected one upstream call after miss, got %d", upstream.getCalls)
	}

	service.now = func() time.Time { return now.Add(time.Minute) }
	secondResponse := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})
	if secondResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected cached status %d, got %d", http.StatusNotFound, secondResponse.StatusCode)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected no second upstream call while negative cache is valid, got %d", upstream.getCalls)
	}
	rawBody, ok := secondResponse.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("expected cached response body type %T, got %T", json.RawMessage{}, secondResponse.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		t.Fatalf("failed to parse cached payload: %v", err)
	}
	if payload["error"] != "Upstream API error" {
		t.Fatalf("expected cached error %q, got %#v", "Upstream API error", payload["error"])
	}
}

func TestServiceHandleExpiredNegativeCacheFallsBackToUpstream(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 25, 0, 0, time.UTC)
	cachedPayload := []byte(`{"error":"Upstream API error","message":{"reason":"missing"}}`)
	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       cachedPayload,
			SourceStatus:  http.StatusNotFound,
			ExpiresAt:     now.Add(-time.Second),
			LastCheckedAt: now,
		},
		hit: true,
	}
	upstream := &fakeUpstreamClient{
		responseStatus: http.StatusOK,
		responseBody:   `{"tag_name":"fresh-upstream"}`,
	}
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

	response := service.Handle(context.Background(), Request{
		RawURL: "https://api.github.com/repos/org/repo/releases/latest",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d after expired negative cache, got %d", http.StatusOK, response.StatusCode)
	}
	if upstream.getCalls != 1 {
		t.Fatalf("expected upstream call when negative cache expired, got %d", upstream.getCalls)
	}
}

func TestServiceHandleSkipsCachingForbiddenAndTransientStatuses(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 22, 30, 0, 0, time.UTC)
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "401 unauthorized", statusCode: http.StatusUnauthorized},
		{name: "403 forbidden", statusCode: http.StatusForbidden},
		{name: "429 too many requests", statusCode: http.StatusTooManyRequests},
		{name: "500 internal server error", statusCode: http.StatusInternalServerError},
		{name: "503 service unavailable", statusCode: http.StatusServiceUnavailable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cacheStore := &fakeCacheStore{}
			upstream := &fakeUpstreamClient{
				responseStatus: tc.statusCode,
				responseBody:   `{"error":"upstream-failure"}`,
			}
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

			response := service.Handle(context.Background(), Request{
				RawURL: "https://api.github.com/repos/org/repo/releases/latest",
			})

			if response.StatusCode != tc.statusCode {
				t.Fatalf("expected status %d, got %d", tc.statusCode, response.StatusCode)
			}
			if upstream.getCalls != 1 {
				t.Fatalf("expected one upstream call, got %d", upstream.getCalls)
			}
			if cacheStore.setCalls != 0 {
				t.Fatalf("expected no cache writes for status %d, got %d", tc.statusCode, cacheStore.setCalls)
			}
		})
	}
}

func TestServiceRefreshInBackgroundCoalescesConcurrentSameKey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 0, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	cacheStore := &fakeCacheStore{
		record: cache.Record{
			Payload:       []byte(`{"tag_name":"cached"}`),
			ETag:          `"etag-cached"`,
			LastCheckedAt: now.Add(-time.Minute),
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

	if started := service.RefreshInBackground(Request{RawURL: rawURL}); !started {
		t.Fatal("expected first background refresh attempt to start")
	}
	upstream.waitStarted(t)

	if started := service.RefreshInBackground(Request{RawURL: rawURL}); started {
		t.Fatal("expected duplicate background refresh attempt to be coalesced")
	}

	upstream.release()
	upstream.waitDone(t)
	cacheStore.waitForSetCalls(t, 1, time.Second)

	if calls := upstream.callCount(); calls != 1 {
		t.Fatalf("expected one upstream call for coalesced refreshes, got %d", calls)
	}
	setCalls, lastSetKey, _ := cacheStore.snapshot()
	if setCalls != 1 {
		t.Fatalf("expected one cache update after 304, got %d", setCalls)
	}
	if lastSetKey != cacheKey {
		t.Fatalf("expected cache key %q, got %q", cacheKey, lastSetKey)
	}
}

func TestServiceHandleFreshHitNotBlockedByInFlightBackgroundRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 23, 5, 0, 0, time.UTC)
	rawURL := "https://api.github.com/repos/org/repo/releases/latest"
	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		t.Fatalf("failed to build cache key: %v", err)
	}

	l1Store := l1.NewCacheWithMaxBytes(1 << 20)
	stored := cache.Record{
		Payload:       []byte(`{"tag_name":"cached-fast-hit"}`),
		ETag:          `"etag-cached"`,
		LastCheckedAt: now,
	}
	l1Store.Set(cacheKey, stored)

	upstream := newBlockingUpstreamClient(http.StatusNotModified, "")
	service := NewService(
		cache.NewFacade(l1Store, nil, nil),
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		upstream,
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.now = func() time.Time { return now }

	if started := service.RefreshInBackground(Request{RawURL: rawURL}); !started {
		t.Fatal("expected background refresh to start")
	}
	upstream.waitStarted(t)

	responseCh := make(chan Response, 1)
	go func() {
		responseCh <- service.Handle(context.Background(), Request{RawURL: rawURL})
	}()

	select {
	case response := <-responseCh:
		if response.StatusCode != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected cache hit response to return without waiting for background refresh")
	}

	upstream.release()
	upstream.waitDone(t)

	if calls := upstream.callCount(); calls != 1 {
		t.Fatalf("expected only one upstream background refresh call, got %d", calls)
	}
}

type fakeCacheStore struct {
	mu         sync.Mutex
	record     cache.Record
	hit        bool
	err        error
	getCalls   int
	setCalls   int
	lastSet    cache.Record
	lastSetKey string
	setSignals chan struct{}
}

func (f *fakeCacheStore) Get(_ context.Context, _ string) (cache.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.getCalls++
	if f.err != nil {
		return cache.Record{}, false, f.err
	}
	if !f.hit {
		return cache.Record{}, false, nil
	}

	record := f.record
	record.Payload = append([]byte(nil), f.record.Payload...)
	return record, true, nil
}

func (f *fakeCacheStore) Set(key string, record cache.Record) (bool, error) {
	f.mu.Lock()
	if f.setSignals == nil {
		f.setSignals = make(chan struct{}, 8)
	}

	f.setCalls++
	f.lastSetKey = key
	f.lastSet = record
	f.lastSet.Payload = append([]byte(nil), record.Payload...)
	f.record = f.lastSet
	f.hit = true

	setSignals := f.setSignals
	f.mu.Unlock()

	select {
	case setSignals <- struct{}{}:
	default:
	}

	return true, nil
}

func (f *fakeCacheStore) snapshot() (int, string, cache.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()

	record := f.lastSet
	record.Payload = append([]byte(nil), f.lastSet.Payload...)
	return f.setCalls, f.lastSetKey, record
}

func (f *fakeCacheStore) waitForSetCalls(t *testing.T, expected int, timeout time.Duration) {
	t.Helper()

	f.mu.Lock()
	if f.setSignals == nil {
		f.setSignals = make(chan struct{}, 8)
	}
	setSignals := f.setSignals
	f.mu.Unlock()

	deadline := time.After(timeout)
	for {
		setCalls, _, _ := f.snapshot()
		if setCalls >= expected {
			return
		}

		select {
		case <-setSignals:
		case <-deadline:
			t.Fatalf("timed out waiting for %d cache writes; got %d", expected, setCalls)
		}
	}
}

type fakeHitSignalRecorder struct {
	calls         int
	lastKey       string
	lastAt        time.Time
	enqueueResult bool
}

func (f *fakeHitSignalRecorder) RecordActivity(key string, hitAt time.Time) bool {
	f.calls++
	f.lastKey = key
	f.lastAt = hitAt
	return f.enqueueResult
}

type fakeDroppingWriteBehind struct {
	enqueueCalls int
	lastKey      string
	lastRecord   cache.Record
}

func (f *fakeDroppingWriteBehind) Enqueue(key string, record cache.Record) bool {
	f.enqueueCalls++
	f.lastKey = key
	f.lastRecord = record
	f.lastRecord.Payload = append([]byte(nil), record.Payload...)
	return false
}

type fakeUpstreamClient struct {
	getCalls        int
	lastHeaders     map[string]string
	responseStatus  int
	responseBody    string
	responseHeaders http.Header
	err             error
}

type countingTokenProvider struct {
	calls int
	token string
}

func (p *countingTokenProvider) NextToken() string {
	p.calls++
	return p.token
}

func (f *fakeUpstreamClient) Get(_ context.Context, _ string, token, etag string) (*http.Response, error) {
	f.getCalls++
	f.lastHeaders = map[string]string{
		"Authorization": "token " + token,
	}
	if etag != "" {
		f.lastHeaders["If-None-Match"] = etag
	}
	if f.err != nil {
		return nil, f.err
	}

	statusCode := f.responseStatus
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	body := f.responseBody
	if body == "" {
		body = `{"tag_name":"upstream"}`
	}
	responseHeaders := make(http.Header, len(f.responseHeaders))
	for key, values := range f.responseHeaders {
		for _, value := range values {
			responseHeaders.Add(key, value)
		}
	}

	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     responseHeaders,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (f *fakeUpstreamClient) ObserveRateLimit(string, *http.Response, upstreamgithub.RateLimitedTokenObserver) {
}

type fakeHydrationL2Store struct {
	records  map[string]cache.Record
	getCalls int
}

func (f *fakeHydrationL2Store) Get(_ context.Context, key string) (*cache.Record, bool, error) {
	f.getCalls++
	record, ok := f.records[key]
	if !ok {
		return nil, false, nil
	}

	cloned := record
	cloned.Payload = append([]byte(nil), record.Payload...)
	return &cloned, true, nil
}

type blockingUpstreamClient struct {
	mu            sync.Mutex
	getCalls      int
	lastHeaders   map[string]string
	responseCode  int
	responseBody  string
	started       chan struct{}
	releaseSignal chan struct{}
	done          chan struct{}
}

func newBlockingUpstreamClient(responseCode int, responseBody string) *blockingUpstreamClient {
	if responseCode == 0 {
		responseCode = http.StatusNotModified
	}
	if responseBody == "" {
		responseBody = `{"tag_name":"ignored"}`
	}

	return &blockingUpstreamClient{
		responseCode:  responseCode,
		responseBody:  responseBody,
		started:       make(chan struct{}),
		releaseSignal: make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (b *blockingUpstreamClient) Get(_ context.Context, _ string, token, etag string) (*http.Response, error) {
	b.mu.Lock()
	b.getCalls++
	b.lastHeaders = map[string]string{
		"Authorization": "token " + token,
	}
	if etag != "" {
		b.lastHeaders["If-None-Match"] = etag
	}
	if b.getCalls == 1 {
		close(b.started)
	}
	b.mu.Unlock()

	<-b.releaseSignal

	statusCode := b.responseCode
	response := &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(b.responseBody)),
	}
	select {
	case <-b.done:
	default:
		close(b.done)
	}

	return response, nil
}

func (b *blockingUpstreamClient) ObserveRateLimit(string, *http.Response, upstreamgithub.RateLimitedTokenObserver) {
}

func (b *blockingUpstreamClient) waitStarted(t *testing.T) {
	t.Helper()

	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream call to start")
	}
}

func (b *blockingUpstreamClient) release() {
	select {
	case <-b.releaseSignal:
	default:
		close(b.releaseSignal)
	}
}

func (b *blockingUpstreamClient) waitDone(t *testing.T) {
	t.Helper()

	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream call to finish")
	}
}

func (b *blockingUpstreamClient) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.getCalls
}
