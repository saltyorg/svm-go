package l1

import (
	"context"

	"svm/internal/cache"
)

// Store adapts the in-memory cache to the version service cache interface.
type Store struct {
	cache *Cache
}

// NewStore creates a direct in-memory cache store.
func NewStore(cache *Cache) *Store {
	return &Store{cache: cache}
}

// Get returns a defensive record copy.
func (s *Store) Get(_ context.Context, key string) (cache.Record, bool, error) {
	if s == nil || s.cache == nil {
		return cache.Record{}, false, nil
	}
	record, hit := s.cache.Get(key)
	return record, hit, nil
}

// GetReadonly returns a record whose payload may alias immutable cache storage.
func (s *Store) GetReadonly(_ context.Context, key string) (cache.Record, bool, error) {
	if s == nil || s.cache == nil {
		return cache.Record{}, false, nil
	}
	record, hit := s.cache.GetReadonly(key)
	return record, hit, nil
}

// Set stores a defensive record copy.
func (s *Store) Set(key string, record cache.Record) (bool, error) {
	if s == nil || s.cache == nil {
		return false, nil
	}
	s.cache.Set(key, record)
	return true, nil
}
