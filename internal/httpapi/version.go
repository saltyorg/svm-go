package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	versionservice "svm/internal/service/version"

	"github.com/valyala/fasthttp"
)

type versionService interface {
	Handle(ctx context.Context, request versionservice.Request) versionservice.Response
}

type clientIPResolver func(ctx *fasthttp.RequestCtx) string

var refreshTrueValue = []byte("true")
var newlineValue = []byte{'\n'}
var upstreamStatusHeaderKey = []byte("X-Upstream-Status")
var upstreamStatusZeroValue = []byte("0")

// NewVersionHandler builds the /version endpoint handler.
func NewVersionHandler(service versionService, resolveClientIP clientIPResolver) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		handleVersion(ctx, service, resolveClientIP)
	}
}

// HandleVersion writes a /version response using the provided service and IP resolver.
func HandleVersion(ctx *fasthttp.RequestCtx, service versionService, resolveClientIP clientIPResolver) {
	handleVersion(ctx, service, resolveClientIP)
}

func handleVersion(ctx *fasthttp.RequestCtx, service versionService, resolveClientIP clientIPResolver) {
	ctx.Response.Header.SetContentType("application/json")
	if service == nil {
		writeJSON(ctx, fasthttp.StatusServiceUnavailable, versionservice.ErrorResponse{
			Error:  "Service unavailable",
			Detail: "version service is not configured",
		})
		return
	}

	clientIP := ""
	if resolveClientIP != nil {
		clientIP = resolveClientIP(ctx)
	}

	response := service.Handle(ctx, versionservice.Request{
		RawURL:       string(ctx.QueryArgs().Peek("url")),
		ClientIP:     clientIP,
		URI:          "/version",
		ForceRefresh: bytes.EqualFold(ctx.QueryArgs().Peek("refresh"), refreshTrueValue),
	})

	cacheStatus := response.CacheStatus
	if cacheStatus == "" {
		cacheStatus = versionservice.CacheStatusBypass
	}
	ctx.Response.Header.Set("X-Cache", cacheStatus)
	ctx.Response.Header.Set("X-Cache-Key", response.CacheKey)
	setUpstreamStatusHeader(&ctx.Response.Header, response.UpstreamStatus)

	if response.StatusCode == 0 {
		response.StatusCode = fasthttp.StatusOK
	}

	writeJSON(ctx, response.StatusCode, response.Body)
}

func writeJSON(ctx *fasthttp.RequestCtx, statusCode int, payload any) {
	ctx.SetStatusCode(statusCode)
	if body, ok := payload.(json.RawMessage); ok && len(body) > 0 {
		_, _ = ctx.Write(body)
		_, _ = ctx.Write(newlineValue)
		return
	}

	_ = json.NewEncoder(ctx).Encode(payload)
}

func setUpstreamStatusHeader(header *fasthttp.ResponseHeader, status int) {
	if header == nil {
		return
	}
	if status == 0 {
		header.SetBytesKV(upstreamStatusHeaderKey, upstreamStatusZeroValue)
		return
	}

	var scratch [12]byte
	value := strconv.AppendInt(scratch[:0], int64(status), 10)
	header.SetBytesKV(upstreamStatusHeaderKey, value)
}
