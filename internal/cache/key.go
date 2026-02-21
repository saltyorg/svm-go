package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
)

const cacheKeyVersionPrefix = "v1:"

var (
	// ErrCacheURLRequired indicates cache key generation was called without a URL.
	ErrCacheURLRequired = errors.New("url is required")
	// ErrCacheURLInvalid indicates cache key generation received an invalid URL.
	ErrCacheURLInvalid = errors.New("invalid url")
)

// NormalizeURL canonicalizes URL components used for cache key derivation.
func NormalizeURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", ErrCacheURLRequired
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrCacheURLInvalid
	}

	return normalizeParsedURL(parsed), nil
}

// KeyFromURL returns a deterministic Redis-safe cache key for a URL.
func KeyFromURL(rawURL string) (string, error) {
	normalized, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}

	return keyFromNormalizedURL(normalized), nil
}

// KeyFromParsedURL returns a deterministic Redis-safe cache key from an already parsed URL.
func KeyFromParsedURL(parsedURL *url.URL) (string, error) {
	if parsedURL == nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", ErrCacheURLInvalid
	}

	return keyFromNormalizedURL(normalizeParsedURL(parsedURL)), nil
}

func keyFromNormalizedURL(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return cacheKeyVersionPrefix + hex.EncodeToString(sum[:])
}

func normalizeParsedURL(parsedURL *url.URL) string {
	parsed := *parsedURL
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = normalizeHost(parsed.Scheme, parsed.Hostname(), parsed.Port())
	parsed.Fragment = ""

	if parsed.Path == "" {
		parsed.Path = "/"
	}

	query := parsed.Query()
	for key := range query {
		sort.Strings(query[key])
	}
	parsed.RawQuery = query.Encode()

	return parsed.String()
}

func normalizeHost(scheme, hostname, port string) string {
	host := strings.ToLower(hostname)
	if port == "" {
		return formatHost(host)
	}

	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return formatHost(host)
	}

	return net.JoinHostPort(host, port)
}

func formatHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}

	return host
}
