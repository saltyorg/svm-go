package cache

import (
	"errors"
	"regexp"
	"testing"
)

func TestNormalizeURLCanonicalizesForCacheKey(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeURL(" HTTPS://API.GITHUB.COM:443/repos/acme/widgets/releases/latest?b=2&a=3&a=1#fragment ")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	want := "https://api.github.com/repos/acme/widgets/releases/latest?a=1&a=3&b=2"
	if normalized != want {
		t.Fatalf("expected normalized URL %q, got %q", want, normalized)
	}
}

func TestNormalizeURLDefaultsPath(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeURL("https://api.github.com")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if normalized != "https://api.github.com/" {
		t.Fatalf("expected normalized URL with default path, got %q", normalized)
	}
}

func TestKeyFromURLIsDeterministicAndRedisSafe(t *testing.T) {
	t.Parallel()

	first, err := KeyFromURL("https://api.github.com/repos/acme/widgets/releases/latest?a=1&b=2")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	second, err := KeyFromURL("https://api.github.com/repos/acme/widgets/releases/latest?b=2&a=1")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if first != second {
		t.Fatalf("expected stable key for equivalent URL, got %q and %q", first, second)
	}

	redisSafe := regexp.MustCompile(`^[a-z0-9:]+$`)
	if !redisSafe.MatchString(first) {
		t.Fatalf("expected redis-safe cache key format, got %q", first)
	}
}

func TestKeyFromURLReturnsErrorForInvalidURL(t *testing.T) {
	t.Parallel()

	_, err := KeyFromURL("://missing-scheme")
	if !errors.Is(err, ErrCacheURLInvalid) {
		t.Fatalf("expected ErrCacheURLInvalid, got %v", err)
	}
}

func TestKeyFromURLEmptyURL(t *testing.T) {
	t.Parallel()

	_, err := KeyFromURL(" ")
	if !errors.Is(err, ErrCacheURLRequired) {
		t.Fatalf("expected ErrCacheURLRequired, got %v", err)
	}
}
