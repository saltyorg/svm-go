package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"svm/internal/security"
)

const defaultTimeout = 30 * time.Second
const (
	defaultMaxIdleConns        = 512
	defaultMaxIdleConnsPerHost = 256
)

// Client wraps outbound GitHub API calls.
type Client struct {
	httpClient *http.Client
}

var ErrRedirectNotAllowed = errors.New("upstream redirect destination is not allowed")

// RateLimitedTokenObserver tracks temporary token cooldowns.
type RateLimitedTokenObserver interface {
	ObserveRateLimitedToken(token string)
}

// NewClient creates a client with default timeout behavior.
func NewClient() *Client {
	return NewClientWithAllowedHosts(nil)
}

// NewClientWithAllowedHosts creates a client that validates every redirect destination.
func NewClientWithAllowedHosts(allowedHosts []string) *Client {
	client := &http.Client{
		Timeout:   defaultTimeout,
		Transport: newDefaultTransport(),
	}
	client.CheckRedirect = redirectValidator(allowedHosts)
	return NewClientWithHTTPClient(client)
}

// NewClientWithHTTPClient creates a client with an injected HTTP client.
func NewClientWithHTTPClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   defaultTimeout,
			Transport: newDefaultTransport(),
		}
	}
	if httpClient.Transport == nil {
		httpClient.Transport = newDefaultTransport()
	}

	return &Client{httpClient: httpClient}
}

func redirectValidator(allowedHosts []string) func(*http.Request, []*http.Request) error {
	hosts := append([]string(nil), allowedHosts...)
	return func(req *http.Request, _ []*http.Request) error {
		if req == nil || req.URL == nil || !redirectURLAllowed(req.URL, hosts) {
			return ErrRedirectNotAllowed
		}
		return nil
	}
}

func redirectURLAllowed(destination *url.URL, allowedHosts []string) bool {
	if destination == nil || destination.User != nil || destination.Scheme != "https" {
		return false
	}
	return len(allowedHosts) == 0 || security.HostAllowed(destination.Hostname(), allowedHosts)
}

// Get performs a GET request with auth and optional conditional request headers.
func (c *Client) Get(ctx context.Context, url, token, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "token "+token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
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

func newDefaultTransport() *http.Transport {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || baseTransport == nil {
		return &http.Transport{
			MaxIdleConns:        defaultMaxIdleConns,
			MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
		}
	}
	transport := baseTransport.Clone()
	transport.MaxIdleConns = defaultMaxIdleConns
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	return transport
}
