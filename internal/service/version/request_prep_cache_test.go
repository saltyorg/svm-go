package version

import "testing"

func TestRequestPrepCacheCachesPreparedResult(t *testing.T) {
	t.Parallel()

	cache := newRequestPrepCache(8)
	var calls int
	prepare := func(rawURL string) preparedRequest {
		calls++
		return preparedRequest{
			urlToFetch: rawURL,
			cacheKey:   "cache-key",
		}
	}

	first := cache.GetOrPrepare("https://api.github.com/repos/org/repo/releases/latest", prepare)
	second := cache.GetOrPrepare("https://api.github.com/repos/org/repo/releases/latest", prepare)

	if calls != 1 {
		t.Fatalf("expected one prepare call, got %d", calls)
	}
	if first != second {
		t.Fatalf("expected cached prepared request %v, got %v", first, second)
	}
}

func TestRequestPrepCacheHonorsMaxEntries(t *testing.T) {
	t.Parallel()

	cache := newRequestPrepCache(1)
	var calls int
	prepare := func(rawURL string) preparedRequest {
		calls++
		return preparedRequest{cacheKey: rawURL}
	}

	cache.GetOrPrepare("url-1", prepare)
	cache.GetOrPrepare("url-2", prepare)
	cache.GetOrPrepare("url-2", prepare)

	if calls != 3 {
		t.Fatalf("expected uncached prepare call after max entries reached, got %d", calls)
	}
}
