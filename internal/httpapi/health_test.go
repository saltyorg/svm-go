package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestHealthHandlerReturnsHealthyWhenServing(t *testing.T) {
	t.Parallel()

	handler := NewHealthHandler(func() bool { return true })

	ctx := newRequestCtx(fasthttp.MethodGet, "/healthz")
	handler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}

	var response healthResponse
	if err := json.Unmarshal(ctx.Response.Body(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Status != "healthy" {
		t.Fatalf("expected status %q, got %q", "healthy", response.Status)
	}
	if len(response.Dependencies) != 1 {
		t.Fatalf("expected only request-path dependency, got %v", response.Dependencies)
	}
}

func TestHealthHandlerReturnsUnhealthyWhenRequestPathCannotServe(t *testing.T) {
	t.Parallel()

	handler := NewHealthHandler(func() bool { return false })

	ctx := newRequestCtx(fasthttp.MethodGet, "/healthz")
	handler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
		t.Fatalf(
			"expected status %d when request path is unavailable, got %d",
			fasthttp.StatusServiceUnavailable,
			ctx.Response.StatusCode(),
		)
	}

	var response healthResponse
	if err := json.Unmarshal(ctx.Response.Body(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Status != "unhealthy" {
		t.Fatalf("expected status %q, got %q", "unhealthy", response.Status)
	}
	if dep := response.Dependencies["request_path"]; dep.Status != "down" {
		t.Fatalf("expected request_path dependency status down, got %q", dep.Status)
	}
}
