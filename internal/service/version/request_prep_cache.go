package version

import (
	"sync"
)

const defaultRequestPrepCacheMaxEntries int64 = 4096

type preparedRequest struct {
	urlToFetch string
	cacheKey   string
	err        error
}

// requestPrepCache memoizes URL validation and key derivation for repeated requests.
type requestPrepCache struct {
	maxEntries int64
	mu         sync.RWMutex
	entries    map[string]preparedRequest
}

func newRequestPrepCache(maxEntries int64) *requestPrepCache {
	if maxEntries <= 0 {
		maxEntries = defaultRequestPrepCacheMaxEntries
	}

	return &requestPrepCache{
		maxEntries: maxEntries,
		entries:    make(map[string]preparedRequest),
	}
}

func (c *requestPrepCache) GetOrPrepare(rawURL string, prepare func(rawURL string) preparedRequest) preparedRequest {
	if c == nil || prepare == nil {
		return prepare(rawURL)
	}

	c.mu.RLock()
	cached, ok := c.entries[rawURL]
	c.mu.RUnlock()
	if ok {
		return cached
	}

	prepared := prepare(rawURL)
	c.mu.Lock()
	if existing, exists := c.entries[rawURL]; exists {
		c.mu.Unlock()
		return existing
	}
	if int64(len(c.entries)) < c.maxEntries {
		c.entries[rawURL] = prepared
	}
	c.mu.Unlock()

	return prepared
}
