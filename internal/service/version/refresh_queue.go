package version

import (
	"context"
	"sync"
)

const defaultRefreshQueueSize = 256

// RefreshJob is a queued refresh request with scheduler-assigned job ID.
type RefreshJob struct {
	ID  uint64
	Key string
}

// RefreshQueue is a bounded queue for async refresh keys.
type RefreshQueue struct {
	mu     sync.RWMutex
	closed bool
	keys   chan RefreshJob
}

// NewRefreshQueue creates a bounded refresh queue.
func NewRefreshQueue(size int) *RefreshQueue {
	if size <= 0 {
		size = defaultRefreshQueueSize
	}

	return &RefreshQueue{
		keys: make(chan RefreshJob, size),
	}
}

// Enqueue attempts to queue a key without blocking.
func (q *RefreshQueue) Enqueue(job RefreshJob) bool {
	if q == nil || job.Key == "" {
		return false
	}

	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return false
	}

	select {
	case q.keys <- job:
		return true
	default:
		return false
	}
}

// Dequeue waits for the next key until context cancellation or queue close.
func (q *RefreshQueue) Dequeue(ctx context.Context) (RefreshJob, bool) {
	if q == nil {
		return RefreshJob{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-ctx.Done():
		return RefreshJob{}, false
	case job, ok := <-q.keys:
		if !ok {
			return RefreshJob{}, false
		}
		return job, true
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
