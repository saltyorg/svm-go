package l1

import (
	"container/list"
	"sync"

	"svm/internal/cache"
)

const recordMetadataOverheadBytes int64 = 128
const bytesPerGiB float64 = 1024 * 1024 * 1024

type entry struct {
	record    cache.Record
	sizeBytes int64
	element   *list.Element
}

// Cache is an in-memory key/value store used as the L1 cache tier.
type Cache struct {
	mu            sync.Mutex
	entries       map[string]*entry
	recency       *list.List
	usageBytes    int64
	maxUsageBytes int64
}

// NewCache creates an empty L1 cache.
func NewCache() *Cache {
	return NewCacheWithMaxGB(cache.DefaultPolicy().L1MaxGB)
}

// NewCacheWithMaxGB creates an empty L1 cache with a custom memory budget.
func NewCacheWithMaxGB(maxGB float64) *Cache {
	maxUsageBytes := int64(maxGB * bytesPerGiB)
	return NewCacheWithMaxBytes(maxUsageBytes)
}

// NewCacheWithMaxBytes creates an empty L1 cache with a custom memory budget.
func NewCacheWithMaxBytes(maxUsageBytes int64) *Cache {
	defaultUsageBytes := int64(cache.DefaultPolicy().L1MaxGB * bytesPerGiB)
	if maxUsageBytes <= 0 {
		maxUsageBytes = defaultUsageBytes
	}

	return &Cache{
		entries:       make(map[string]*entry),
		recency:       list.New(),
		maxUsageBytes: maxUsageBytes,
	}
}

// Get returns a record copy by key.
func (c *Cache) Get(key string) (cache.Record, bool) {
	if c == nil || key == "" {
		return cache.Record{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	stored, ok := c.entries[key]
	if !ok {
		return cache.Record{}, false
	}

	c.recency.MoveToFront(stored.element)
	return cloneRecord(stored.record), true
}

// GetReadonly returns a record by key and may alias payload bytes to cache storage.
func (c *Cache) GetReadonly(key string) (cache.Record, bool) {
	if c == nil || key == "" {
		return cache.Record{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	stored, ok := c.entries[key]
	if !ok {
		return cache.Record{}, false
	}

	c.recency.MoveToFront(stored.element)
	return stored.record, true
}

// Set upserts a record and updates estimated memory accounting.
func (c *Cache) Set(key string, record cache.Record) {
	if c == nil || key == "" {
		return
	}

	cloned := cloneRecord(record)
	sizeBytes := estimateEntryUsage(cloned)

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[key]; ok {
		c.usageBytes -= existing.sizeBytes
		existing.record = cloned
		existing.sizeBytes = sizeBytes
		c.usageBytes += sizeBytes
		c.recency.MoveToFront(existing.element)
		c.evictToBudgetLocked()
		return
	}

	element := c.recency.PushFront(key)
	c.entries[key] = &entry{
		record:    cloned,
		sizeBytes: sizeBytes,
		element:   element,
	}
	c.usageBytes += sizeBytes
	c.evictToBudgetLocked()
}

// EstimatedUsageBytes reports estimated memory used by entries.
func (c *Cache) EstimatedUsageBytes() int64 {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.usageBytes
}

// Len returns the number of stored keys.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.entries)
}

func (c *Cache) evictToBudgetLocked() {
	for c.usageBytes > c.maxUsageBytes {
		oldest := c.recency.Back()
		if oldest == nil {
			return
		}

		key, ok := oldest.Value.(string)
		c.recency.Remove(oldest)
		if !ok {
			continue
		}

		existing, found := c.entries[key]
		if !found {
			continue
		}

		delete(c.entries, key)
		c.usageBytes -= existing.sizeBytes
		if c.usageBytes < 0 {
			c.usageBytes = 0
		}
	}
}

func cloneRecord(record cache.Record) cache.Record {
	cloned := record
	cloned.Payload = append([]byte(nil), record.Payload...)
	return cloned
}

func estimateEntryUsage(record cache.Record) int64 {
	return int64(len(record.Payload)) + recordMetadataOverheadBytes
}
