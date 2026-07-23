package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
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
var ErrPrivateAddressNotAllowed = errors.New("upstream resolved to a private or special-purpose address")

var authenticatedHosts = map[string]struct{}{
	"api.github.com": {},
}

var nonPublicNetworks = mustNetworks(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"2001:db8::/32",
)

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
	if port := destination.Port(); port != "" && port != "443" {
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

	if token != "" && shouldAuthenticate(req.URL) {
		req.Header.Set("Authorization", "token "+token)
	}
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
			DialContext:         safeDialContext,
		}
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = defaultMaxIdleConns
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	transport.DialContext = safeDialContext
	return transport
}

func shouldAuthenticate(destination *url.URL) bool {
	if destination == nil {
		return false
	}
	_, ok := authenticatedHosts[strings.ToLower(destination.Hostname())]
	return ok
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse upstream address: %w", err)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve upstream host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("upstream host resolved to no addresses")
	}
	for _, resolved := range addresses {
		if !isPublicIP(resolved.IP) {
			return nil, ErrPrivateAddressNotAllowed
		}
	}

	dialer := &net.Dialer{Timeout: defaultTimeout, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, resolved := range addresses {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("dial upstream host: %w", lastErr)
}

func isPublicIP(ip net.IP) bool {
	if ip == nil ||
		!ip.IsGlobalUnicast() ||
		ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, network := range nonPublicNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func mustNetworks(values ...string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		networks = append(networks, network)
	}
	return networks
}
