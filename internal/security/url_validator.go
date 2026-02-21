package security

import (
	"errors"
	"net/url"
	"strings"
)

var (
	// ErrURLRequired indicates that the input URL was empty.
	ErrURLRequired = errors.New("url parameter is required")
	// ErrInvalidURL indicates that the input could not be parsed as an absolute URL.
	ErrInvalidURL = errors.New("url parameter is invalid")
	// ErrHTTPSOnly indicates that the URL scheme is not HTTPS.
	ErrHTTPSOnly = errors.New("url scheme must be https")
	// ErrUpstreamHostNotAllowed indicates that the parsed host is outside the allowlist.
	ErrUpstreamHostNotAllowed = errors.New("upstream host is not allowlisted")
)

// ParseHTTPSURL parses and validates a request URL for upstream fetching.
func ParseHTTPSURL(rawURL string) (*url.URL, error) {
	return parseHTTPSURL(rawURL)
}

// ParseHTTPSURLWithAllowlist parses and validates a request URL and enforces an allowlist when provided.
func ParseHTTPSURLWithAllowlist(rawURL string, allowedHosts []string) (*url.URL, error) {
	parsedURL, err := parseHTTPSURL(rawURL)
	if err != nil {
		return nil, err
	}

	if len(allowedHosts) > 0 && !hostAllowed(parsedURL.Hostname(), allowedHosts) {
		return nil, ErrUpstreamHostNotAllowed
	}

	return parsedURL, nil
}

func parseHTTPSURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, ErrURLRequired
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL == nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, ErrInvalidURL
	}

	if !strings.EqualFold(parsedURL.Scheme, "https") {
		return nil, ErrHTTPSOnly
	}

	return parsedURL, nil
}

func hostAllowed(host string, allowedHosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}

	for _, allowed := range allowedHosts {
		if host == strings.ToLower(strings.TrimSpace(allowed)) {
			return true
		}
	}

	return false
}
