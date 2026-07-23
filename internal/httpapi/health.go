package httpapi

import (
	"encoding/json"

	"github.com/valyala/fasthttp"
)

type requestServingCheckFunc func() bool

type healthDependency struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type healthResponse struct {
	Status       string                      `json:"status"`
	Dependencies map[string]healthDependency `json:"dependencies"`
}

// NewHealthHandler reports whether the request-serving path is ready.
func NewHealthHandler(requestServingCheck requestServingCheckFunc) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.SetContentType("application/json")

		serving := isRequestServing(requestServingCheck)

		response := healthResponse{
			Dependencies: map[string]healthDependency{
				"request_path": {Status: "up"},
			},
		}
		if !serving {
			response.Dependencies["request_path"] = healthDependency{
				Status: "down",
				Error:  "request path is not ready",
			}
		}
		statusCode := fasthttp.StatusOK
		if !serving {
			response.Status = "unhealthy"
			statusCode = fasthttp.StatusServiceUnavailable
		} else {
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
