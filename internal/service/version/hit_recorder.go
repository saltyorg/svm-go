package version

import (
	"context"
	"io"
	"time"

	"svm/internal/observability"
)

const defaultHitRecorderQueueSize = 256

// ActiveKeyIndexer defines active-key index persistence and lookup behavior.
type ActiveKeyIndexer interface {
	UpsertActiveKey(ctx context.Context, key string, lastHitAt time.Time) error
	ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error)
}

type hitSignal struct {
	key   string
	hitAt time.Time
}

// HitRecorder asynchronously records cache-hit signals to the active-key index.
type HitRecorder struct {
	index  ActiveKeyIndexer
	logger *observability.Logger
	now    func() time.Time

	queue  chan hitSignal
	cancel context.CancelFunc
	done   chan struct{}
}

// NewHitRecorder creates a background hit recorder with non-blocking enqueue semantics.
func NewHitRecorder(index ActiveKeyIndexer, queueSize int, logger *observability.Logger) *HitRecorder {
	if queueSize <= 0 {
		queueSize = defaultHitRecorderQueueSize
	}
	if logger == nil {
		logger = observability.NewLogger(io.Discard)
	}

	ctx, cancel := context.WithCancel(context.Background())
	recorder := &HitRecorder{
		index:  index,
		logger: logger,
		now:    time.Now,
		queue:  make(chan hitSignal, queueSize),
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go recorder.run(ctx)
	return recorder
}

// RecordHit attempts to enqueue a hit signal without blocking the request path.
func (r *HitRecorder) RecordHit(key string, hitAt time.Time) bool {
	if r == nil || r.index == nil || key == "" {
		return false
	}
	if hitAt.IsZero() {
		hitAt = r.now()
	}

	signal := hitSignal{
		key:   key,
		hitAt: hitAt.UTC(),
	}
	select {
	case r.queue <- signal:
		return true
	default:
		return false
	}
}

// ActiveKeysSince delegates weekly-lookback key lookup to the active-key index.
func (r *HitRecorder) ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error) {
	if r == nil || r.index == nil {
		return nil, nil
	}

	return r.index.ActiveKeysSince(ctx, since, limit)
}

// Close stops the recorder worker.
func (r *HitRecorder) Close() {
	if r == nil || r.cancel == nil {
		return
	}

	r.cancel()
	<-r.done
}

func (r *HitRecorder) run(ctx context.Context) {
	defer close(r.done)

	for {
		select {
		case <-ctx.Done():
			return
		case signal := <-r.queue:
			if err := r.index.UpsertActiveKey(context.Background(), signal.key, signal.hitAt); err != nil {
				r.logger.Warn(
					"failed to upsert active key",
					observability.String("key", signal.key),
					observability.String("error", err.Error()),
				)
			}
		}
	}
}
