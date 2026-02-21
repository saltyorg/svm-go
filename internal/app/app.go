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

// ShutdownDrainer defines bounded shutdown drain behavior for background workers.
type ShutdownDrainer interface {
	CloseWithDrain(timeout time.Duration)
}

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

// DrainOnShutdown invokes drainer shutdown hooks with a shared timeout budget.
func DrainOnShutdown(timeout time.Duration, drainers ...ShutdownDrainer) {
	if timeout <= 0 {
		return
	}

	for _, drainer := range drainers {
		if drainer == nil {
			continue
		}
		drainer.CloseWithDrain(timeout)
	}
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
