package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/valyala/fasthttp"
)

const healthCheckTimeout = time.Second

type redisPingFunc func(ctx context.Context) error
type requestServingCheckFunc func() bool

type healthDependency struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type healthResponse struct {
	Status       string                      `json:"status"`
	Dependencies map[string]healthDependency `json:"dependencies"`
}

// NewHealthHandler reports service health and dependency state.
func NewHealthHandler(redisPing redisPingFunc, requestServingCheck requestServingCheckFunc) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.SetContentType("application/json")

		serving := isRequestServing(requestServingCheck)
		redisErr := checkRedis(redisPing)

		response := healthResponse{
			Dependencies: map[string]healthDependency{
				"request_path": {Status: "up"},
				"redis":        {Status: "up"},
			},
		}
		if !serving {
			response.Dependencies["request_path"] = healthDependency{
				Status: "down",
				Error:  "request path is not ready",
			}
		}
		if redisErr != nil {
			response.Dependencies["redis"] = healthDependency{
				Status: "down",
				Error:  redisErr.Error(),
			}
		}

		statusCode := fasthttp.StatusOK
		switch {
		case !serving:
			response.Status = "unhealthy"
			statusCode = fasthttp.StatusServiceUnavailable
		case redisErr != nil:
			response.Status = "degraded"
		default:
			response.Status = "healthy"
		}

		ctx.SetStatusCode(statusCode)
		_ = json.NewEncoder(ctx).Encode(response)
	}
}

func isRequestServing(check requestServingCheckFunc) bool {
	if check == nil {
		return true
	}
	return check()
}

func checkRedis(ping redisPingFunc) error {
	if ping == nil {
		return errors.New("redis health check is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	if err := ping(ctx); err != nil {
		return err
	}
	return nil
}
