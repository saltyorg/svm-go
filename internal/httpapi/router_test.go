package httpapi

import (
	"net"
	"testing"

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

func TestWithAccessLogInvokesHookAfterHandler(t *testing.T) {
	t.Parallel()

	var called bool
	var gotMethod string
	var gotPath string
	var gotStatus int

	handler := func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(fasthttp.StatusCreated)
	}

	wrapped := WithAccessLog(handler, func(ctx *fasthttp.RequestCtx) {
		called = true
		gotMethod = string(ctx.Method())
		gotPath = string(ctx.Path())
		gotStatus = ctx.Response.StatusCode()
	})

	ctx := newRequestCtx(fasthttp.MethodGet, "/ping")
	wrapped(ctx)

	if !called {
		t.Fatal("expected access-log hook to be called")
	}
	if gotMethod != fasthttp.MethodGet {
		t.Fatalf("expected method %q, got %q", fasthttp.MethodGet, gotMethod)
	}
	if gotPath != "/ping" {
		t.Fatalf("expected path %q, got %q", "/ping", gotPath)
	}
	if gotStatus != fasthttp.StatusCreated {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusCreated, gotStatus)
	}
}

func TestNewRouterRoutesVersionAndPing(t *testing.T) {
	t.Parallel()

	var versionCalled bool
	var pingCalled bool

	router := NewRouter(
		func(ctx *fasthttp.RequestCtx) {
			versionCalled = true
			ctx.SetStatusCode(fasthttp.StatusAccepted)
		},
		func(ctx *fasthttp.RequestCtx) {
			pingCalled = true
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
	)

	versionCtx := newRequestCtx(fasthttp.MethodGet, "/version")
	router(versionCtx)
	if !versionCalled {
		t.Fatal("expected /version handler to be called")
	}
	if versionCtx.Response.StatusCode() != fasthttp.StatusAccepted {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusAccepted, versionCtx.Response.StatusCode())
	}

	pingCtx := newRequestCtx(fasthttp.MethodGet, "/ping")
	router(pingCtx)
	if !pingCalled {
		t.Fatal("expected /ping handler to be called")
	}
	if pingCtx.Response.StatusCode() != fasthttp.StatusNoContent {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusNoContent, pingCtx.Response.StatusCode())
	}
}

func TestNewRouterReturnsMethodNotAllowedForNonGET(t *testing.T) {
	t.Parallel()

	var versionCalled bool

	router := NewRouter(
		func(ctx *fasthttp.RequestCtx) {
			versionCalled = true
			ctx.SetStatusCode(fasthttp.StatusAccepted)
		},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
	)

	ctx := newRequestCtx(fasthttp.MethodPost, "/version")
	router(ctx)

	if versionCalled {
		t.Fatal("expected /version handler not to be called for non-GET")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusMethodNotAllowed, ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.Peek("Allow")); got != fasthttp.MethodGet {
		t.Fatalf("expected Allow %q, got %q", fasthttp.MethodGet, got)
	}
}

func TestNewRouterReturnsNotFoundForUnknownPath(t *testing.T) {
	t.Parallel()

	router := NewRouter(
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
	)

	ctx := newRequestCtx(fasthttp.MethodGet, "/does-not-exist")
	router(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusNotFound, ctx.Response.StatusCode())
	}
}

func TestNewRouterRoutesHealthz(t *testing.T) {
	t.Parallel()

	var healthCalled bool

	router := NewRouter(
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {
			healthCalled = true
			ctx.SetStatusCode(fasthttp.StatusOK)
		},
		func(ctx *fasthttp.RequestCtx) {},
	)

	ctx := newRequestCtx(fasthttp.MethodGet, "/healthz")
	router(ctx)

	if !healthCalled {
		t.Fatal("expected /healthz handler to be called")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}
}

func TestNewRouterRoutesMetrics(t *testing.T) {
	t.Parallel()

	var metricsCalled bool

	router := NewRouter(
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {},
		func(ctx *fasthttp.RequestCtx) {
			metricsCalled = true
			ctx.SetStatusCode(fasthttp.StatusOK)
		},
	)

	ctx := newRequestCtx(fasthttp.MethodGet, "/metrics")
	router(ctx)

	if !metricsCalled {
		t.Fatal("expected /metrics handler to be called")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}
}
