package httpapi

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/cache/l1"
	versionservice "svm/internal/service/version"

	"github.com/valyala/fasthttp"
)

var (
	benchmarkVersionResponseSink versionservice.Response
	benchmarkStatusCodeSink      int
	benchmarkRemoteAddr          = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
)

func BenchmarkVersionServiceCacheHit(b *testing.B) {
	service, cleanup := newBenchmarkVersionService(b, benchmarkVersionURL)
	b.Cleanup(cleanup)

	request := versionservice.Request{
		RawURL:   benchmarkVersionURL,
		ClientIP: "127.0.0.1:12345",
		URI:      "/version",
	}

	response := service.Handle(context.Background(), request)
	if response.StatusCode != http.StatusOK {
		b.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if response.CacheStatus != versionservice.CacheStatusHit {
		b.Fatalf("expected cache status %q, got %q", versionservice.CacheStatusHit, response.CacheStatus)
	}

	b.ReportAllocs()
	b.ResetTimer()

	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		benchmarkVersionResponseSink = service.Handle(ctx, request)
	}
}

func BenchmarkVersionEndpointCacheHit(b *testing.B) {
	service, cleanup := newBenchmarkVersionService(b, benchmarkVersionURL)
	b.Cleanup(cleanup)

	router := NewRouter(
		NewVersionHandler(service, benchmarkResolveClientIP),
		func(*fasthttp.RequestCtx) {},
		func(*fasthttp.RequestCtx) {},
		func(*fasthttp.RequestCtx) {},
	)

	ctx := newBenchmarkRequestCtx(fasthttp.MethodGet, benchmarkVersionRequestURI)

	router(ctx)
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		b.Fatalf("expected status %d, got %d", fasthttp.StatusOK, got)
	}
	if got := string(ctx.Response.Header.Peek("X-Cache")); got != versionservice.CacheStatusHit {
		b.Fatalf("expected X-Cache %q, got %q", versionservice.CacheStatusHit, got)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ctx.Request.SetRequestURI(benchmarkVersionRequestURI)
		ctx.Response.Reset()
		router(ctx)
	}
	benchmarkStatusCodeSink = ctx.Response.StatusCode()
}

const benchmarkVersionURL = "https://api.github.com/repos/hashicorp/terraform/releases/latest"
const benchmarkVersionRequestURI = "/version?url=" + benchmarkVersionURL

func newBenchmarkVersionService(b *testing.B, rawURL string) (*versionservice.Service, func()) {
	b.Helper()

	policy := cache.DefaultPolicy()
	now := time.Now().UTC()
	record := cache.Record{
		Payload:       []byte(`{"tag_name":"v1.8.5"}`),
		URL:           rawURL,
		FetchedAt:     now,
		LastHitAt:     now,
		LastCheckedAt: now,
		ExpiresAt:     now.Add(policy.HardTTL),
		SourceStatus:  http.StatusOK,
	}

	cacheKey, err := cache.KeyFromURL(rawURL)
	if err != nil {
		b.Fatalf("failed to build cache key: %v", err)
	}

	store := cache.NewFacade(l1.NewCacheWithMaxBytes(1<<20), nil, nil)
	if _, err := store.Set(cacheKey, record); err != nil {
		b.Fatalf("failed to warm cache: %v", err)
	}

	hitRecorder := versionservice.NewHitRecorder(benchmarkActiveKeyIndex{}, 1024, nil)
	service := versionservice.NewService(
		store,
		hitRecorder,
		nil,
		nil,
		nil,
		nil,
		policy,
	)

	return service, hitRecorder.Close
}

func newBenchmarkRequestCtx(method, uri string) *fasthttp.RequestCtx {
	req := &fasthttp.Request{}
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(req, benchmarkRemoteAddr, nil)
	return ctx
}

func benchmarkResolveClientIP(ctx *fasthttp.RequestCtx) string {
	if ctx == nil || ctx.RemoteAddr() == nil {
		return ""
	}
	return ctx.RemoteAddr().String()
}

type benchmarkActiveKeyIndex struct{}

func (benchmarkActiveKeyIndex) UpsertActiveKey(context.Context, string, time.Time) error {
	return nil
}

func (benchmarkActiveKeyIndex) ActiveKeysSince(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}
