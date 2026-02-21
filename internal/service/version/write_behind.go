package version

import (
	"container/list"
	"context"
	"io"
	"sync"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
)

const (
	defaultWriteBehindQueueSize        = 256
	defaultWriteBehindFlushInterval    = time.Second
	defaultWriteBehindRetryMaxInterval = 30 * time.Second
	defaultWriteBehindRetryMaxAge      = 5 * time.Minute
	defaultShutdownDrainTimeout        = 2 * time.Second
)

// CacheRecordPersister defines persistence for cache records.
type CacheRecordPersister interface {
	Persist(ctx context.Context, key string, record *cache.Record) error
}

type pendingWrite struct {
	key             string
	record          cache.Record
	firstEnqueuedAt time.Time
	nextAttemptAt   time.Time
	attempts        int
	element         *list.Element
}

// WriteBehind persists cache mutations asynchronously with bounded retries.
type WriteBehind struct {
	persister CacheRecordPersister
	logger    *observability.Logger
	now       func() time.Time

	flushInterval    time.Duration
	retryMaxInterval time.Duration
	retryMaxAge      time.Duration
	maxPending       int

	mu      sync.Mutex
	pending map[string]*pendingWrite
	order   *list.List
	closed  bool

	cancel context.CancelFunc
	done   chan struct{}
}

// NewWriteBehind creates a write-behind queue and starts its worker loop.
func NewWriteBehind(
	persister CacheRecordPersister,
	queueSize int,
	flushInterval time.Duration,
	retryMaxInterval time.Duration,
	retryMaxAge time.Duration,
	logger *observability.Logger,
) *WriteBehind {
	return newWriteBehind(
		persister,
		queueSize,
		flushInterval,
		retryMaxInterval,
		retryMaxAge,
		logger,
		time.Now,
		true,
	)
}

func newWriteBehind(
	persister CacheRecordPersister,
	queueSize int,
	flushInterval time.Duration,
	retryMaxInterval time.Duration,
	retryMaxAge time.Duration,
	logger *observability.Logger,
	now func() time.Time,
	autoStart bool,
) *WriteBehind {
	if queueSize <= 0 {
		queueSize = defaultWriteBehindQueueSize
	}
	if flushInterval <= 0 {
		flushInterval = defaultWriteBehindFlushInterval
	}
	if retryMaxInterval <= 0 {
		retryMaxInterval = defaultWriteBehindRetryMaxInterval
	}
	if retryMaxAge <= 0 {
		retryMaxAge = defaultWriteBehindRetryMaxAge
	}
	if logger == nil {
		logger = observability.NewLogger(io.Discard)
	}
	if now == nil {
		now = time.Now
	}

	writer := &WriteBehind{
		persister:        persister,
		logger:           logger,
		now:              now,
		flushInterval:    flushInterval,
		retryMaxInterval: retryMaxInterval,
		retryMaxAge:      retryMaxAge,
		maxPending:       queueSize,
		pending:          make(map[string]*pendingWrite, queueSize),
		order:            list.New(),
		done:             make(chan struct{}),
	}

	if !autoStart {
		close(writer.done)
		return writer
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer.cancel = cancel
	go writer.run(ctx)

	return writer
}

// Enqueue records a cache mutation without blocking the request path.
func (w *WriteBehind) Enqueue(key string, record cache.Record) bool {
	if w == nil || w.persister == nil || key == "" {
		return false
	}

	now := w.now().UTC()
	cloned := cloneWriteBehindRecord(record)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}

	if existing, ok := w.pending[key]; ok {
		existing.record = cloned
		existing.nextAttemptAt = now
		existing.attempts = 0
		w.order.MoveToBack(existing.element)
		return true
	}

	if w.order.Len() >= w.maxPending {
		w.dropOldestLocked()
	}

	entry := &pendingWrite{
		key:             key,
		record:          cloned,
		firstEnqueuedAt: now,
		nextAttemptAt:   now,
	}
	entry.element = w.order.PushBack(entry)
	w.pending[key] = entry

	return true
}

// Close stops the worker loop.
func (w *WriteBehind) Close() {
	w.CloseWithDrain(defaultShutdownDrainTimeout)
}

// CloseWithDrain stops the worker loop and drains queued events up to timeout.
func (w *WriteBehind) CloseWithDrain(timeout time.Duration) {
	if w == nil {
		return
	}
	if timeout <= 0 {
		timeout = defaultShutdownDrainTimeout
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), timeout)
	defer cancelDrain()

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-drainCtx.Done():
			return
		}
	}

	w.drainPending(drainCtx)
}

func (w *WriteBehind) run(ctx context.Context) {
	defer close(w.done)

	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.flushOnce(ctx)
		}
	}
}

func (w *WriteBehind) flushOnce(ctx context.Context) {
	if w == nil || w.persister == nil {
		return
	}

	for {
		now := w.now().UTC()
		entry := w.popReadyLocked(now)
		if entry == nil {
			return
		}

		if now.Sub(entry.firstEnqueuedAt) > w.retryMaxAge {
			w.logger.Warn(
				"dropping stale write-behind event",
				observability.String("key", entry.key),
			)
			continue
		}

		record := cloneWriteBehindRecord(entry.record)
		err := w.persister.Persist(ctx, entry.key, &record)
		if err == nil {
			continue
		}

		w.logger.Warn(
			"failed to persist write-behind event",
			observability.String("key", entry.key),
			observability.String("error", err.Error()),
		)
		w.requeueAfterFailure(entry, now, false)
	}
}

func (w *WriteBehind) popReadyLocked(now time.Time) *pendingWrite {
	w.mu.Lock()
	defer w.mu.Unlock()

	for node := w.order.Front(); node != nil; node = node.Next() {
		entry := node.Value.(*pendingWrite)
		if entry.nextAttemptAt.After(now) {
			continue
		}

		w.order.Remove(node)
		delete(w.pending, entry.key)
		entry.element = nil
		return entry
	}

	return nil
}

func (w *WriteBehind) popOldestLocked() *pendingWrite {
	w.mu.Lock()
	defer w.mu.Unlock()

	node := w.order.Front()
	if node == nil {
		return nil
	}

	entry := node.Value.(*pendingWrite)
	w.order.Remove(node)
	delete(w.pending, entry.key)
	entry.element = nil
	return entry
}

func (w *WriteBehind) requeueAfterFailure(entry *pendingWrite, now time.Time, allowWhenClosed bool) {
	if entry == nil {
		return
	}

	entry.attempts++
	entry.nextAttemptAt = now.Add(nextRetryDelay(entry.attempts, w.flushInterval, w.retryMaxInterval))

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed && !allowWhenClosed {
		return
	}
	if _, exists := w.pending[entry.key]; exists {
		return
	}
	if w.order.Len() >= w.maxPending {
		w.dropOldestLocked()
	}

	entry.element = w.order.PushBack(entry)
	w.pending[entry.key] = entry
}

func (w *WriteBehind) drainPending(ctx context.Context) {
	if w == nil || w.persister == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		now := w.now().UTC()
		entry := w.popOldestLocked()
		if entry == nil {
			return
		}

		if now.Sub(entry.firstEnqueuedAt) > w.retryMaxAge {
			w.logger.Warn(
				"dropping stale write-behind event during shutdown drain",
				observability.String("key", entry.key),
			)
			continue
		}

		record := cloneWriteBehindRecord(entry.record)
		if err := w.persister.Persist(ctx, entry.key, &record); err != nil {
			w.logger.Warn(
				"failed to persist write-behind event during shutdown drain",
				observability.String("key", entry.key),
				observability.String("error", err.Error()),
			)
		}
	}
}

func (w *WriteBehind) dropOldestLocked() {
	node := w.order.Front()
	if node == nil {
		return
	}

	entry := node.Value.(*pendingWrite)
	w.order.Remove(node)
	delete(w.pending, entry.key)

	w.logger.Warn(
		"dropping oldest write-behind event",
		observability.String("key", entry.key),
	)
}

func nextRetryDelay(attempt int, base time.Duration, max time.Duration) time.Duration {
	if base <= 0 {
		base = defaultWriteBehindFlushInterval
	}
	if max <= 0 {
		max = defaultWriteBehindRetryMaxInterval
	}
	if base > max {
		return max
	}
	if attempt <= 1 {
		return base
	}

	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
		if delay >= max {
			return max
		}
	}

	return delay
}

func cloneWriteBehindRecord(record cache.Record) cache.Record {
	cloned := record
	cloned.Payload = append([]byte(nil), record.Payload...)
	return cloned
}
