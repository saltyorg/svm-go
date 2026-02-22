package httpapi

import (
	"fmt"
	"strings"

	"svm/internal/observability"

	"github.com/valyala/fasthttp"
)

const prometheusTextContentType = "text/plain; version=0.0.4; charset=utf-8"

// NewMetricsHandler serves low-cardinality process metrics in Prometheus format.
func NewMetricsHandler(metrics *observability.Metrics) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.Response.Header.SetContentType(prometheusTextContentType)

		snapshot := metrics.Snapshot()

		var builder strings.Builder
		writeCounter(&builder, "svm_cache_hits_total", "Total number of cache hits.", snapshot.CacheHits)
		writeCounter(&builder, "svm_cache_misses_total", "Total number of cache misses.", snapshot.CacheMisses)
		writeCounter(&builder, "svm_revalidate_runs_total", "Total number of revalidation runs.", snapshot.RevalidateRuns)
		writeCounter(&builder, "svm_upstream_requests_total", "Total number of upstream requests.", snapshot.UpstreamRequests)
		writeCounter(&builder, "svm_upstream_errors_total", "Total number of upstream errors.", snapshot.UpstreamErrors)
		writeCounter(&builder, "svm_refresh_updated_total", "Total number of background refreshes that updated cache records.", snapshot.RefreshUpdated)
		writeCounter(&builder, "svm_refresh_not_modified_total", "Total number of background refreshes resolved by 304 Not Modified.", snapshot.RefreshNotMod)
		writeCounter(&builder, "svm_refresh_skipped_total", "Total number of background refreshes skipped without an upstream call.", snapshot.RefreshSkipped)
		writeCounter(&builder, "svm_refresh_failed_total", "Total number of background refreshes that failed.", snapshot.RefreshFailed)
		writeCounter(&builder, "svm_refresh_processed_total", "Total number of background refresh queue jobs processed by workers.", snapshot.RefreshProcessed)

		_, _ = ctx.WriteString(builder.String())
	}
}

func writeCounter(builder *strings.Builder, name, help string, value uint64) {
	_, _ = fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	_, _ = fmt.Fprintf(builder, "# TYPE %s counter\n", name)
	_, _ = fmt.Fprintf(builder, "%s %d\n", name, value)
}
