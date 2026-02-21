package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"svm/internal/cache"
	"svm/internal/security"
	versionservice "svm/internal/service/version"

	"github.com/valyala/fasthttp"
)

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

func TestVersionHandlerReturnsBadRequestForInvalidURL(t *testing.T) {
	t.Parallel()

	versionHandler := NewVersionHandler(
		versionservice.NewService(nil, nil, nil, nil, nil, nil, cache.DefaultPolicy()),
		nil,
	)
	router := NewRouter(versionHandler, noopHandler, noopHandler, noopHandler)

	ctx := newRequestCtx(fasthttp.MethodGet, "/version?url=:::")
	router(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusBadRequest, got)
	}

	var response versionservice.ErrorResponse
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

func TestVersionHandlerReturnsSuccessPayload(t *testing.T) {
	t.Parallel()

	service := &fakeVersionService{
		result: versionservice.Response{
			StatusCode: fasthttp.StatusOK,
			Body:       map[string]string{"status": "ok"},
		},
	}
	versionHandler := NewVersionHandler(service, func(*fasthttp.RequestCtx) string {
		return "127.0.0.1:12345"
	})
	router := NewRouter(versionHandler, noopHandler, noopHandler, noopHandler)

	ctx := newRequestCtx(
		fasthttp.MethodGet,
		"/version?url=https://api.github.com/repos/hashicorp/terraform/releases/latest",
	)
	router(ctx)

	if !service.called {
		t.Fatal("expected version service to be called")
	}
	if service.request.RawURL != "https://api.github.com/repos/hashicorp/terraform/releases/latest" {
		t.Fatalf("unexpected raw url: %q", service.request.RawURL)
	}
	if service.request.URI != "/version" {
		t.Fatalf("unexpected uri: %q", service.request.URI)
	}
	if service.request.ClientIP != "127.0.0.1:12345" {
		t.Fatalf("unexpected client ip: %q", service.request.ClientIP)
	}
	if service.request.ForceRefresh {
		t.Fatal("expected force refresh to default to false")
	}
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusOK, got)
	}

	var payload map[string]string
	if err := json.Unmarshal(ctx.Response.Body(), &payload); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", payload["status"])
	}
}

func TestVersionHandlerSetsCacheOutcomeHeaders(t *testing.T) {
	t.Parallel()

	service := &fakeVersionService{
		result: versionservice.Response{
			StatusCode:     fasthttp.StatusOK,
			Body:           map[string]string{"status": "ok"},
			CacheStatus:    versionservice.CacheStatusRevalidated,
			CacheKey:       "cache:v1:test-key",
			UpstreamStatus: fasthttp.StatusNotModified,
		},
	}
	versionHandler := NewVersionHandler(service, nil)
	router := NewRouter(versionHandler, noopHandler, noopHandler, noopHandler)

	ctx := newRequestCtx(fasthttp.MethodGet, "/version?url=https://api.github.com/repos/hashicorp/terraform/releases/latest")
	router(ctx)

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

func noopHandler(*fasthttp.RequestCtx) {}
