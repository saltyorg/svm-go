package httpapi

import (
	"strings"
	"testing"

	"svm/internal/observability"

	"github.com/valyala/fasthttp"
)

func TestMetricsHandlerReturnsPrometheusText(t *testing.T) {
	t.Parallel()

	metrics := observability.NewMetrics()
	metrics.IncCacheHit()
	metrics.IncCacheMiss()
	metrics.IncRevalidateRun()
	metrics.IncUpstreamRequest()
	metrics.IncUpstreamError()

	handler := NewMetricsHandler(metrics)

	ctx := newRequestCtx(fasthttp.MethodGet, "/metrics")
	handler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.ContentType()); got != prometheusTextContentType {
		t.Fatalf("expected content type %q, got %q", prometheusTextContentType, got)
	}

	body := string(ctx.Response.Body())
	assertContainsLine(t, body, "svm_cache_hits_total 1")
	assertContainsLine(t, body, "svm_cache_misses_total 1")
	assertContainsLine(t, body, "svm_revalidate_runs_total 1")
	assertContainsLine(t, body, "svm_upstream_requests_total 1")
	assertContainsLine(t, body, "svm_upstream_errors_total 1")
}

func assertContainsLine(t *testing.T, body, expected string) {
	t.Helper()
	if !strings.Contains(body, expected) {
		t.Fatalf("expected body to contain %q, got %q", expected, body)
	}
}
