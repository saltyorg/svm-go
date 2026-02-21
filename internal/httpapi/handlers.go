package httpapi

import (
	"github.com/valyala/fasthttp"
)

type handlers struct {
	version fasthttp.RequestHandler
	ping    fasthttp.RequestHandler
	health  fasthttp.RequestHandler
	metrics fasthttp.RequestHandler
}

func newHandlers(versionHandler, pingHandler, healthHandler, metricsHandler fasthttp.RequestHandler) handlers {
	return handlers{
		version: versionHandler,
		ping:    pingHandler,
		health:  healthHandler,
		metrics: metricsHandler,
	}
}
