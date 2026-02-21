package httpapi

import (
	"github.com/valyala/fasthttp"
)

// AccessLogHook runs after the router finishes writing the response.
type AccessLogHook func(ctx *fasthttp.RequestCtx)

// NewRouter registers the HTTP routes used by the service.
func NewRouter(
	versionHandler, pingHandler, healthHandler, metricsHandler fasthttp.RequestHandler,
) fasthttp.RequestHandler {
	routes := newHandlers(versionHandler, pingHandler, healthHandler, metricsHandler)

	return func(ctx *fasthttp.RequestCtx) {
		switch string(ctx.Path()) {
		case "/version":
			if !ctx.IsGet() {
				writeMethodNotAllowed(ctx)
				return
			}
			routes.version(ctx)
		case "/ping":
			if !ctx.IsGet() {
				writeMethodNotAllowed(ctx)
				return
			}
			routes.ping(ctx)
		case "/healthz":
			if !ctx.IsGet() {
				writeMethodNotAllowed(ctx)
				return
			}
			routes.health(ctx)
		case "/metrics":
			if !ctx.IsGet() {
				writeMethodNotAllowed(ctx)
				return
			}
			routes.metrics(ctx)
		default:
			ctx.NotFound()
		}
	}
}

func writeMethodNotAllowed(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.Set("Allow", fasthttp.MethodGet)
	ctx.SetStatusCode(fasthttp.StatusMethodNotAllowed)
	ctx.SetBodyString("Method Not Allowed")
}

// WithAccessLog applies a post-response access-log hook.
func WithAccessLog(next fasthttp.RequestHandler, hook AccessLogHook) fasthttp.RequestHandler {
	if hook == nil {
		return next
	}

	return func(ctx *fasthttp.RequestCtx) {
		next(ctx)
		hook(ctx)
	}
}
