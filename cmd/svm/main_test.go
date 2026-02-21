package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"svm/internal/cache"
	"svm/internal/httpapi"
	"svm/internal/observability"
	"svm/internal/security"
	versionservice "svm/internal/service/version"
	upstreamgithub "svm/internal/upstream/github"

	"github.com/valyala/fasthttp"
)

func newRequestCtx(method, uri string) *fasthttp.RequestCtx {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(req, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}, nil)
	return ctx
}

type fakeVersionService struct {
	called  bool
	request versionservice.Request
	result  versionservice.Response
}

func (f *fakeVersionService) Handle(_ context.Context, request versionservice.Request) versionservice.Response {
	f.called = true
	f.request = request
	return f.result
}

func TestVersionHandlerRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	application := &App{}
	ctx := newRequestCtx(fasthttp.MethodGet, "/version?url=:::")

	application.versionHandler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	}

	var response ErrorResponse
	if err := json.Unmarshal(ctx.Response.Body(), &response); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if response.Error != "Invalid parameter" {
		t.Fatalf("expected error %q, got %q", "Invalid parameter", response.Error)
	}
	if response.Detail != security.ErrInvalidURL.Error() {
		t.Fatalf("expected detail %q, got %q", security.ErrInvalidURL.Error(), response.Detail)
	}
}

func TestVersionHandlerRejectsDisallowedHost(t *testing.T) {
	t.Parallel()

	application := &App{allowedHosts: []string{"api.github.com"}}
	ctx := newRequestCtx(fasthttp.MethodGet, "/version?url=https://example.com/releases/latest")

	application.versionHandler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	}

	var response ErrorResponse
	if err := json.Unmarshal(ctx.Response.Body(), &response); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if response.Error != "Invalid parameter" {
		t.Fatalf("expected error %q, got %q", "Invalid parameter", response.Error)
	}
	if response.Detail != security.ErrUpstreamHostNotAllowed.Error() {
		t.Fatalf("expected detail %q, got %q", security.ErrUpstreamHostNotAllowed.Error(), response.Detail)
	}
}

func TestVersionHandlerDelegatesToVersionService(t *testing.T) {
	t.Parallel()

	fakeService := &fakeVersionService{
		result: versionservice.Response{
			StatusCode: fasthttp.StatusTeapot,
			Body:       map[string]string{"status": "delegated"},
		},
	}
	application := &App{versionService: fakeService}
	ctx := newRequestCtx(fasthttp.MethodGet, "/version?url=https://api.github.com/repos/hashicorp/terraform/releases/latest")

	application.versionHandler(ctx)

	if !fakeService.called {
		t.Fatal("expected version service to be called")
	}
	if fakeService.request.RawURL != "https://api.github.com/repos/hashicorp/terraform/releases/latest" {
		t.Fatalf("unexpected raw url: %q", fakeService.request.RawURL)
	}
	if fakeService.request.URI != "/version" {
		t.Fatalf("unexpected uri: %q", fakeService.request.URI)
	}
	if fakeService.request.ClientIP != "127.0.0.1:12345" {
		t.Fatalf("unexpected client ip: %q", fakeService.request.ClientIP)
	}
	if fakeService.request.ForceRefresh {
		t.Fatal("expected force refresh to default to false")
	}
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusTeapot {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusTeapot, got)
	}
}

func TestVersionHandlerParsesRefreshQueryOverride(t *testing.T) {
	t.Parallel()

	fakeService := &fakeVersionService{
		result: versionservice.Response{
			StatusCode: fasthttp.StatusOK,
			Body:       map[string]string{"status": "ok"},
		},
	}
	application := &App{versionService: fakeService}
	ctx := newRequestCtx(
		fasthttp.MethodGet,
		"/version?url=https://api.github.com/repos/hashicorp/terraform/releases/latest&refresh=true",
	)

	application.versionHandler(ctx)

	if !fakeService.called {
		t.Fatal("expected version service to be called")
	}
	if !fakeService.request.ForceRefresh {
		t.Fatal("expected force refresh to be true when refresh=true is provided")
	}
}

func TestVersionHandlerSetsCacheOutcomeHeaders(t *testing.T) {
	t.Parallel()

	fakeService := &fakeVersionService{
		result: versionservice.Response{
			StatusCode:     fasthttp.StatusOK,
			Body:           map[string]string{"status": "ok"},
			CacheStatus:    versionservice.CacheStatusRevalidated,
			CacheKey:       "cache:v1:test-key",
			UpstreamStatus: fasthttp.StatusNotModified,
		},
	}
	application := &App{versionService: fakeService}
	ctx := newRequestCtx(fasthttp.MethodGet, "/version?url=https://api.github.com/repos/hashicorp/terraform/releases/latest")

	application.versionHandler(ctx)

	if got := string(ctx.Response.Header.Peek("X-Cache")); got != versionservice.CacheStatusRevalidated {
		t.Fatalf("expected X-Cache %q, got %q", versionservice.CacheStatusRevalidated, got)
	}
	if got := string(ctx.Response.Header.Peek("X-Cache-Key")); got != "cache:v1:test-key" {
		t.Fatalf("expected X-Cache-Key %q, got %q", "cache:v1:test-key", got)
	}
	if got := string(ctx.Response.Header.Peek("X-Upstream-Status")); got != "304" {
		t.Fatalf("expected X-Upstream-Status %q, got %q", "304", got)
	}
}

func TestVersionAndMetricsHandlersExposeRuntimeCounterUpdates(t *testing.T) {
	t.Parallel()

	metrics := observability.NewMetrics()
	service := versionservice.NewService(
		&fakeRuntimeCacheStore{},
		nil,
		upstreamgithub.NewRoundRobinTokenProvider([]string{"token-a"}),
		&fakeMetricsUpstream{
			statusCode: http.StatusOK,
			body:       `{"tag_name":"v1.0.0"}`,
		},
		nil,
		nil,
		cache.DefaultPolicy(),
	)
	service.SetMetrics(metrics)

	application := &App{
		versionService: service,
		metrics:        metrics,
	}

	versionCtx := newRequestCtx(
		fasthttp.MethodGet,
		"/version?url=https://api.github.com/repos/hashicorp/terraform/releases/latest",
	)
	application.versionHandler(versionCtx)

	if got := versionCtx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusOK, got)
	}

	metricsCtx := newRequestCtx(fasthttp.MethodGet, "/metrics")
	httpapi.NewMetricsHandler(application.metrics)(metricsCtx)

	body := string(metricsCtx.Response.Body())
	if !strings.Contains(body, "svm_cache_misses_total 1") {
		t.Fatalf("expected metrics body to include cache miss increment, got %q", body)
	}
	if !strings.Contains(body, "svm_upstream_requests_total 1") {
		t.Fatalf("expected metrics body to include upstream request increment, got %q", body)
	}
}

type fakeRefreshCapableService struct {
	fakeVersionService

	mu          sync.Mutex
	refreshKeys []string
}

type fakeMetricsUpstream struct {
	statusCode int
	body       string
}

func (f *fakeMetricsUpstream) Get(_ context.Context, _ string, _ map[string]string) (*http.Response, error) {
	statusCode := f.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	body := f.body
	if body == "" {
		body = `{"tag_name":"upstream"}`
	}

	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (f *fakeMetricsUpstream) ObserveRateLimit(string, *http.Response, upstreamgithub.RateLimitedTokenObserver) {
}

func (f *fakeRefreshCapableService) RefreshByKey(cacheKey string) bool {
	f.mu.Lock()
	f.refreshKeys = append(f.refreshKeys, cacheKey)
	f.mu.Unlock()
	return true
}

func (f *fakeRefreshCapableService) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.refreshKeys)
}

type fakeRuntimeCacheStore struct {
	record cache.Record
	hit    bool
}

func (f *fakeRuntimeCacheStore) Get(_ context.Context, _ string) (cache.Record, bool, error) {
	return f.record, f.hit, nil
}

func (f *fakeRuntimeCacheStore) Set(_ string, _ cache.Record) (bool, error) {
	return true, nil
}

type fakeActiveKeyIndex struct {
	keys []string
}

func (f *fakeActiveKeyIndex) UpsertActiveKey(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (f *fakeActiveKeyIndex) ActiveKeysSince(_ context.Context, _ time.Time, _ int) ([]string, error) {
	return append([]string(nil), f.keys...), nil
}

func TestStartRevalidationRuntimeWiresSchedulerQueueAndWorkers(t *testing.T) {
	t.Parallel()

	policy := cache.DefaultPolicy()
	policy.RevalidateInterval = time.Minute
	policy.RevalidatePerWorkerRPS = 1000
	policy.WriteBehindQueueSize = 8

	refresher := &fakeRefreshCapableService{}
	cacheStore := &fakeRuntimeCacheStore{
		hit: true,
		record: cache.Record{
			LastCheckedAt: time.Time{},
			URL:           "https://api.github.com/repos/hashicorp/terraform/releases/latest",
		},
	}
	hitRecorder := versionservice.NewHitRecorder(&fakeActiveKeyIndex{
		keys: []string{"cache:v1:refresh-key"},
	}, 8, nil)
	defer hitRecorder.Close()

	application := &App{
		cachePolicy:    policy,
		versionService: refresher,
		cacheStore:     cacheStore,
		hitRecorder:    hitRecorder,
	}

	application.startRevalidationRuntime()
	defer application.revalidateScheduler.Close()
	defer application.refreshWorkers.Close()
	defer application.refreshQueue.Close()

	if application.refreshQueue == nil {
		t.Fatal("expected refresh queue to be initialized")
	}
	if application.revalidateScheduler == nil {
		t.Fatal("expected scheduler to be initialized")
	}
	if application.refreshWorkers == nil {
		t.Fatal("expected refresh workers to be initialized")
	}

	result := application.revalidateScheduler.Sweep(context.Background())
	if result.DueKeys != 1 {
		t.Fatalf("expected one due key, got %d", result.DueKeys)
	}
	if result.Enqueued != 1 {
		t.Fatalf("expected one enqueued key, got %d", result.Enqueued)
	}

	deadline := time.After(time.Second)
	for refresher.refreshCount() < 1 {
		select {
		case <-deadline:
			t.Fatal("expected refresh workers to process queued key")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestStartRevalidationRuntimeSkipsWhenServiceDoesNotSupportRefreshByKey(t *testing.T) {
	t.Parallel()

	policy := cache.DefaultPolicy()
	hitRecorder := versionservice.NewHitRecorder(&fakeActiveKeyIndex{}, 4, nil)
	defer hitRecorder.Close()

	application := &App{
		cachePolicy:    policy,
		versionService: &fakeVersionService{},
		cacheStore:     &fakeRuntimeCacheStore{},
		hitRecorder:    hitRecorder,
	}

	application.startRevalidationRuntime()

	if application.refreshQueue != nil {
		t.Fatal("expected refresh queue to remain nil")
	}
	if application.revalidateScheduler != nil {
		t.Fatal("expected scheduler to remain nil")
	}
	if application.refreshWorkers != nil {
		t.Fatal("expected refresh workers to remain nil")
	}
}

func TestShutdownBackgroundClosesRuntimeComponents(t *testing.T) {
	t.Parallel()

	drainer := &fakeWriteBehindRuntime{}
	hitRecorder := &fakeHitRecorderRuntime{}
	scheduler := &fakeSchedulerRuntime{}
	workers := &fakeWorkersRuntime{}
	queue := &fakeQueueRuntime{}

	application := &App{
		cachePolicy: cache.Policy{
			ShutdownDrainTimeout: 75 * time.Millisecond,
		},
		hitRecorder:         hitRecorder,
		writeBehind:         drainer,
		refreshQueue:        queue,
		revalidateScheduler: scheduler,
		refreshWorkers:      workers,
	}

	application.shutdownBackground(application.shutdownDrainTimeout())

	if scheduler.closeCalls != 1 {
		t.Fatalf("expected scheduler close once, got %d", scheduler.closeCalls)
	}
	if workers.closeCalls != 1 {
		t.Fatalf("expected workers close once, got %d", workers.closeCalls)
	}
	if queue.closeCalls != 1 {
		t.Fatalf("expected queue close once, got %d", queue.closeCalls)
	}
	if hitRecorder.closeCalls != 1 {
		t.Fatalf("expected hit recorder close once, got %d", hitRecorder.closeCalls)
	}
	if drainer.closeWithDrainCalls != 1 {
		t.Fatalf("expected write-behind drain once, got %d", drainer.closeWithDrainCalls)
	}
	if drainer.lastTimeout != 75*time.Millisecond {
		t.Fatalf("expected drain timeout 75ms, got %s", drainer.lastTimeout)
	}
}

func TestRunServerWithLifecycleHandlesSignalAndTriggersShutdownSequence(t *testing.T) {
	t.Parallel()

	server := &fakeGracefulShutdownServer{
		shutdownStarted: make(chan struct{}, 1),
	}
	drainer := &fakeWriteBehindRuntime{}
	hitRecorder := &fakeHitRecorderRuntime{}
	scheduler := &fakeSchedulerRuntime{}
	workers := &fakeWorkersRuntime{}
	queue := &fakeQueueRuntime{}
	application := &App{
		cachePolicy: cache.Policy{
			ShutdownDrainTimeout: 120 * time.Millisecond,
		},
		hitRecorder:         hitRecorder,
		writeBehind:         drainer,
		refreshQueue:        queue,
		revalidateScheduler: scheduler,
		refreshWorkers:      workers,
	}

	signalCh := make(chan os.Signal, 1)
	signalCh <- syscall.SIGTERM

	done := make(chan error, 1)
	go func() {
		done <- runServerWithLifecycle(
			application,
			server,
			signalCh,
			func() error {
				<-server.shutdownStarted
				return nil
			},
		)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal-driven lifecycle shutdown")
	}

	if server.shutdownCalls != 1 {
		t.Fatalf("expected one server shutdown call, got %d", server.shutdownCalls)
	}
	if !server.deadlineSet {
		t.Fatal("expected shutdown context to include a deadline")
	}
	if scheduler.closeCalls != 1 {
		t.Fatalf("expected scheduler close once, got %d", scheduler.closeCalls)
	}
	if workers.closeCalls != 1 {
		t.Fatalf("expected workers close once, got %d", workers.closeCalls)
	}
	if queue.closeCalls != 1 {
		t.Fatalf("expected queue close once, got %d", queue.closeCalls)
	}
	if hitRecorder.closeCalls != 1 {
		t.Fatalf("expected hit recorder close once, got %d", hitRecorder.closeCalls)
	}
	if drainer.closeWithDrainCalls != 1 {
		t.Fatalf("expected write-behind drain once, got %d", drainer.closeWithDrainCalls)
	}
	if drainer.lastTimeout != 120*time.Millisecond {
		t.Fatalf("expected drain timeout 120ms, got %s", drainer.lastTimeout)
	}
}

func TestRunServerWithLifecycleClosesRuntimeWhenListenFails(t *testing.T) {
	t.Parallel()

	drainer := &fakeWriteBehindRuntime{}
	hitRecorder := &fakeHitRecorderRuntime{}
	scheduler := &fakeSchedulerRuntime{}
	workers := &fakeWorkersRuntime{}
	queue := &fakeQueueRuntime{}
	application := &App{
		cachePolicy: cache.Policy{
			ShutdownDrainTimeout: 90 * time.Millisecond,
		},
		hitRecorder:         hitRecorder,
		writeBehind:         drainer,
		refreshQueue:        queue,
		revalidateScheduler: scheduler,
		refreshWorkers:      workers,
	}

	listenErr := errors.New("bind failed")
	err := runServerWithLifecycle(
		application,
		&fakeGracefulShutdownServer{},
		make(chan os.Signal),
		func() error {
			return listenErr
		},
	)

	if !errors.Is(err, listenErr) {
		t.Fatalf("expected listen error %v, got %v", listenErr, err)
	}
	if scheduler.closeCalls != 1 {
		t.Fatalf("expected scheduler close once, got %d", scheduler.closeCalls)
	}
	if workers.closeCalls != 1 {
		t.Fatalf("expected workers close once, got %d", workers.closeCalls)
	}
	if queue.closeCalls != 1 {
		t.Fatalf("expected queue close once, got %d", queue.closeCalls)
	}
	if hitRecorder.closeCalls != 1 {
		t.Fatalf("expected hit recorder close once, got %d", hitRecorder.closeCalls)
	}
	if drainer.closeWithDrainCalls != 1 {
		t.Fatalf("expected write-behind drain once, got %d", drainer.closeWithDrainCalls)
	}
}

type fakeWriteBehindRuntime struct {
	mu                  sync.Mutex
	closeWithDrainCalls int
	lastTimeout         time.Duration
}

func (f *fakeWriteBehindRuntime) Enqueue(string, cache.Record) bool {
	return true
}

func (f *fakeWriteBehindRuntime) CloseWithDrain(timeout time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeWithDrainCalls++
	f.lastTimeout = timeout
}

type fakeHitRecorderRuntime struct {
	mu         sync.Mutex
	closeCalls int
}

func (f *fakeHitRecorderRuntime) RecordHit(string, time.Time) bool {
	return true
}

func (f *fakeHitRecorderRuntime) ActiveKeysSince(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

func (f *fakeHitRecorderRuntime) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
}

type fakeSchedulerRuntime struct {
	mu         sync.Mutex
	closeCalls int
}

func (f *fakeSchedulerRuntime) Sweep(context.Context) versionservice.RevalidateSweepResult {
	return versionservice.RevalidateSweepResult{}
}

func (f *fakeSchedulerRuntime) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
}

type fakeWorkersRuntime struct {
	mu         sync.Mutex
	closeCalls int
}

func (f *fakeWorkersRuntime) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
}

type fakeQueueRuntime struct {
	mu         sync.Mutex
	closeCalls int
}

func (f *fakeQueueRuntime) Enqueue(string) bool {
	return true
}

func (f *fakeQueueRuntime) Dequeue(context.Context) (string, bool) {
	return "", false
}

func (f *fakeQueueRuntime) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
}

type fakeGracefulShutdownServer struct {
	mu              sync.Mutex
	shutdownCalls   int
	deadlineSet     bool
	shutdownStarted chan struct{}
}

func (f *fakeGracefulShutdownServer) ShutdownWithContext(ctx context.Context) error {
	f.mu.Lock()
	f.shutdownCalls++
	_, f.deadlineSet = ctx.Deadline()
	if f.shutdownStarted != nil {
		select {
		case f.shutdownStarted <- struct{}{}:
		default:
		}
	}
	f.mu.Unlock()

	return nil
}
