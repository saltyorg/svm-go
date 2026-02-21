package github

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client wraps outbound GitHub API calls.
type Client struct {
	httpClient *http.Client
}

// RateLimitedTokenObserver tracks temporary token cooldowns.
type RateLimitedTokenObserver interface {
	ObserveRateLimitedToken(token string)
}

// NewClient creates a client with default timeout behavior.
func NewClient() *Client {
	return NewClientWithHTTPClient(&http.Client{Timeout: defaultTimeout})
}

// NewClientWithHTTPClient creates a client with an injected HTTP client.
func NewClientWithHTTPClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}

	return &Client{httpClient: httpClient}
}

// BuildRequestHeaders builds auth and optional conditional request headers.
func BuildRequestHeaders(token string, etag string) map[string]string {
	headers := map[string]string{
		"Authorization": fmt.Sprintf("token %s", token),
	}
	if etag != "" {
		headers["If-None-Match"] = etag
	}
	return headers
}

// Get performs a GET request with optional headers.
func (c *Client) Get(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	return c.httpClient.Do(req)
}

// ObserveRateLimit marks the token on cooldown for 403/429 responses.
func (c *Client) ObserveRateLimit(token string, response *http.Response, observer RateLimitedTokenObserver) {
	if c == nil || token == "" || response == nil || observer == nil {
		return
	}
	if isRateLimitedStatus(response.StatusCode) {
		observer.ObserveRateLimitedToken(token)
	}
}

func isRateLimitedStatus(statusCode int) bool {
	return statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests
}
