package version

import (
	"context"
	"sort"
	"sync"
	"time"
)

// ActivityTracker records cache-key activity in process for revalidation scheduling.
type ActivityTracker struct {
	mu       sync.RWMutex
	lastHits map[string]time.Time
}

// NewActivityTracker creates an empty in-memory activity tracker.
func NewActivityTracker() *ActivityTracker {
	return &ActivityTracker{lastHits: make(map[string]time.Time)}
}

// RecordActivity updates the latest observed access time for a cache key.
func (t *ActivityTracker) RecordActivity(key string, hitAt time.Time) bool {
	if t == nil || key == "" {
		return false
	}
	if hitAt.IsZero() {
		hitAt = time.Now()
	}
	hitAt = hitAt.UTC()

	t.mu.Lock()
	if previous, ok := t.lastHits[key]; !ok || hitAt.After(previous) {
		t.lastHits[key] = hitAt
	}
	t.mu.Unlock()
	return true
}

// ActiveKeysSince returns active keys ordered by their most recent access time.
func (t *ActivityTracker) ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error) {
	if t == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	type activeKey struct {
		key   string
		hitAt time.Time
	}

	t.mu.RLock()
	active := make([]activeKey, 0, len(t.lastHits))
	for key, hitAt := range t.lastHits {
		if !hitAt.Before(since) {
			active = append(active, activeKey{key: key, hitAt: hitAt})
		}
	}
	t.mu.RUnlock()

	sort.Slice(active, func(i, j int) bool {
		if active[i].hitAt.Equal(active[j].hitAt) {
			return active[i].key < active[j].key
		}
		return active[i].hitAt.Before(active[j].hitAt)
	})
	if limit > 0 && len(active) > limit {
		active = active[:limit]
	}

	keys := make([]string, 0, len(active))
	for _, item := range active {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			keys = append(keys, item.key)
		}
	}
	return keys, nil
}

// PruneActiveKeysBefore removes entries outside the configured lookback.
func (t *ActivityTracker) PruneActiveKeysBefore(_ context.Context, cutoff time.Time) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	for key, hitAt := range t.lastHits {
		if hitAt.Before(cutoff) {
			delete(t.lastHits, key)
		}
	}
	t.mu.Unlock()
	return nil
}
