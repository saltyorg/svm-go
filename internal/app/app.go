package app

import (
	"time"

	"github.com/valyala/fasthttp"
)

const (
	defaultReadTimeout  = 30 * time.Second
	defaultWriteTimeout = 30 * time.Second
	defaultIdleTimeout  = 60 * time.Second
)

// ShutdownCloser defines close behavior for components that don't support drains.
type ShutdownCloser interface {
	Close()
}

// NewServer provides a central place for baseline HTTP server wiring.
func NewServer(handler fasthttp.RequestHandler) *fasthttp.Server {
	return &fasthttp.Server{
		Handler:      handler,
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
		IdleTimeout:  defaultIdleTimeout,
	}
}

// ListenAndServe starts the server instance.
func ListenAndServe(server *fasthttp.Server, addr string) error {
	return server.ListenAndServe(addr)
}

// CloseOnShutdown invokes close hooks for background workers.
func CloseOnShutdown(closers ...ShutdownCloser) {
	for _, closer := range closers {
		if closer == nil {
			continue
		}
		closer.Close()
	}
}
