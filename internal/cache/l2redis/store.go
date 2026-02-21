package l2redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"svm/internal/cache"

	"github.com/redis/go-redis/v9"
)

const (
	defaultRecordTTL = 24 * time.Hour
	recordKeyPrefix  = "svm:cache:v1:"
	activeIndexKey   = "svm:cache:v1:active_keys"
)

var (
	// ErrStoreNotConfigured indicates the store was used without a Redis client.
	ErrStoreNotConfigured = errors.New("redis store client is not configured")
	// ErrCacheKeyRequired indicates a cache operation was attempted without a key.
	ErrCacheKeyRequired = errors.New("cache key is required")
	// ErrCacheRecordRequired indicates persistence was attempted with a nil record.
	ErrCacheRecordRequired = errors.New("cache record is required")
)

type redisGetter interface {
	Get(ctx context.Context, key string) *redis.StringCmd
}

type redisSetter interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
}

type redisZSetWriter interface {
	ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd
}

type redisZSetReader interface {
	ZRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) *redis.StringSliceCmd
}

type redisReadWriter interface {
	redisGetter
	redisSetter
	redisZSetWriter
	redisZSetReader
}

// Store manages L2 record persistence and hydration via Redis.
type Store struct {
	client redisReadWriter
	ttl    time.Duration
}

// NewStore builds a Redis-backed cache store with default TTL behavior.
func NewStore(client redisReadWriter) *Store {
	return NewStoreWithTTL(client, defaultRecordTTL)
}

// NewStoreWithTTL builds a Redis-backed cache store with the provided TTL.
func NewStoreWithTTL(client redisReadWriter, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = defaultRecordTTL
	}

	return &Store{
		client: client,
		ttl:    ttl,
	}
}

// Get returns a persisted cache record by key for L1 miss hydration.
func (s *Store) Get(ctx context.Context, key string) (*cache.Record, bool, error) {
	if s == nil || s.client == nil {
		return nil, false, ErrStoreNotConfigured
	}
	if key == "" {
		return nil, false, ErrCacheKeyRequired
	}

	raw, err := s.client.Get(ctx, namespacedRecordKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("redis get record: %w", err)
	}

	record, err := decodeRecord(raw)
	if err != nil {
		return nil, false, err
	}

	return record, true, nil
}

// Persist stores a cache record in Redis using the adapter persistence format.
func (s *Store) Persist(ctx context.Context, key string, record *cache.Record) error {
	if s == nil || s.client == nil {
		return ErrStoreNotConfigured
	}
	if key == "" {
		return ErrCacheKeyRequired
	}
	if record == nil {
		return ErrCacheRecordRequired
	}

	payload, err := encodeRecord(record)
	if err != nil {
		return err
	}

	if err := s.client.Set(ctx, namespacedRecordKey(key), payload, s.ttl).Err(); err != nil {
		return fmt.Errorf("redis set record: %w", err)
	}

	return nil
}

// UpsertActiveKey records a key hit in the active-key index keyed by last-hit timestamp.
func (s *Store) UpsertActiveKey(ctx context.Context, key string, lastHitAt time.Time) error {
	if s == nil || s.client == nil {
		return ErrStoreNotConfigured
	}
	if key == "" {
		return ErrCacheKeyRequired
	}

	member := redis.Z{
		Score:  float64(lastHitAt.UTC().Unix()),
		Member: key,
	}
	if err := s.client.ZAdd(ctx, activeIndexKey, member).Err(); err != nil {
		return fmt.Errorf("redis upsert active key: %w", err)
	}

	return nil
}

// ActiveKeysSince returns keys hit since the provided timestamp, oldest-first by hit time.
func (s *Store) ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error) {
	if s == nil || s.client == nil {
		return nil, ErrStoreNotConfigured
	}

	query := &redis.ZRangeBy{
		Min: fmt.Sprintf("%d", since.UTC().Unix()),
		Max: "+inf",
	}
	if limit > 0 {
		query.Count = int64(limit)
	}

	keys, err := s.client.ZRangeByScore(ctx, activeIndexKey, query).Result()
	if err != nil {
		return nil, fmt.Errorf("redis list active keys: %w", err)
	}

	return keys, nil
}

func namespacedRecordKey(key string) string {
	return recordKeyPrefix + key
}

func encodeRecord(record *cache.Record) ([]byte, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode cache record: %w", err)
	}

	return payload, nil
}

func decodeRecord(payload []byte) (*cache.Record, error) {
	var record cache.Record
	if err := json.Unmarshal(payload, &record); err != nil {
		return nil, fmt.Errorf("decode cache record: %w", err)
	}

	return &record, nil
}
