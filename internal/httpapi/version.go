package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	versionservice "svm/internal/service/version"

	"github.com/valyala/fasthttp"
)

type versionService interface {
	Handle(ctx context.Context, request versionservice.Request) versionservice.Response
}

type clientIPResolver func(ctx *fasthttp.RequestCtx) string

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
		RawURL:   string(ctx.QueryArgs().Peek("url")),
		ClientIP: clientIP,
		URI:      string(ctx.Path()),
		ForceRefresh: strings.EqualFold(
			string(ctx.QueryArgs().Peek("refresh")),
			"true",
		),
	})

	cacheStatus := response.CacheStatus
	if cacheStatus == "" {
		cacheStatus = versionservice.CacheStatusBypass
	}
	ctx.Response.Header.Set("X-Cache", cacheStatus)
	ctx.Response.Header.Set("X-Cache-Key", response.CacheKey)
	ctx.Response.Header.Set("X-Upstream-Status", strconv.Itoa(response.UpstreamStatus))

	if response.StatusCode == 0 {
		response.StatusCode = fasthttp.StatusOK
	}

	writeJSON(ctx, response.StatusCode, response.Body)
}

func writeJSON(ctx *fasthttp.RequestCtx, statusCode int, payload any) {
	ctx.SetStatusCode(statusCode)
	_ = json.NewEncoder(ctx).Encode(payload)
}
