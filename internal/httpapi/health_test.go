package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestHealthHandlerReturnsHealthyWhenServingAndRedisUp(t *testing.T) {
	t.Parallel()

	handler := NewHealthHandler(
		func(context.Context) error { return nil },
		func() bool { return true },
	)

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
	if dep := response.Dependencies["redis"]; dep.Status != "up" {
		t.Fatalf("expected redis dependency status up, got %q", dep.Status)
	}
}

func TestHealthHandlerReturnsDegradedWhenRedisDownButServing(t *testing.T) {
	t.Parallel()

	handler := NewHealthHandler(
		func(context.Context) error { return errors.New("redis unavailable") },
		func() bool { return true },
	)

	ctx := newRequestCtx(fasthttp.MethodGet, "/healthz")
	handler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status %d for degraded health, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}

	var response healthResponse
	if err := json.Unmarshal(ctx.Response.Body(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Status != "degraded" {
		t.Fatalf("expected status %q, got %q", "degraded", response.Status)
	}
	if dep := response.Dependencies["redis"]; dep.Status != "down" {
		t.Fatalf("expected redis dependency status down, got %q", dep.Status)
	}
}

func TestHealthHandlerReturnsUnhealthyWhenRequestPathCannotServe(t *testing.T) {
	t.Parallel()

	handler := NewHealthHandler(
		func(context.Context) error { return nil },
		func() bool { return false },
	)

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
