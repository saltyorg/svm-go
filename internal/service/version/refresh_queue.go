package version

import (
	"context"
	"sync"
)

const defaultRefreshQueueSize = 256

// RefreshQueue is a bounded queue for async refresh keys.
type RefreshQueue struct {
	mu     sync.RWMutex
	closed bool
	keys   chan string
}

// NewRefreshQueue creates a bounded refresh queue.
func NewRefreshQueue(size int) *RefreshQueue {
	if size <= 0 {
		size = defaultRefreshQueueSize
	}

	return &RefreshQueue{
		keys: make(chan string, size),
	}
}

// Enqueue attempts to queue a key without blocking.
func (q *RefreshQueue) Enqueue(key string) bool {
	if q == nil || key == "" {
		return false
	}

	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return false
	}

	select {
	case q.keys <- key:
		return true
	default:
		return false
	}
}

// Dequeue waits for the next key until context cancellation or queue close.
func (q *RefreshQueue) Dequeue(ctx context.Context) (string, bool) {
	if q == nil {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-ctx.Done():
		return "", false
	case key, ok := <-q.keys:
		if !ok {
			return "", false
		}
		return key, true
	}
}

// Close closes the queue and causes Dequeue to stop.
func (q *RefreshQueue) Close() {
	if q == nil {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.keys)
}
