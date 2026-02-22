package version

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
)

const defaultRefreshWorkerCount = 1

type refreshDequeuer interface {
	Dequeue(ctx context.Context) (string, bool)
}

type refreshByKeyRequester interface {
	RefreshByKey(cacheKey string) bool
}

// RefreshWorkers run async refresh jobs from the bounded refresh queue.
type RefreshWorkers struct {
	queue              refreshDequeuer
	refresher          refreshByKeyRequester
	logger             *observability.Logger
	now                func() time.Time
	minInterval        time.Duration
	initialWorkerCount int
	countUpdates       chan int
	metrics            atomic.Pointer[observability.Metrics]

	cancel context.CancelFunc
	done   chan struct{}
}

// NewRefreshWorkers creates and starts refresh workers with per-worker rate limiting.
func NewRefreshWorkers(
	queue refreshDequeuer,
	refresher refreshByKeyRequester,
	workerCount int,
	perWorkerRPS int,
	logger *observability.Logger,
) *RefreshWorkers {
	return newRefreshWorkers(
		queue,
		refresher,
		workerCount,
		perWorkerRPS,
		logger,
		time.Now,
		true,
	)
}

func newRefreshWorkers(
	queue refreshDequeuer,
	refresher refreshByKeyRequester,
	workerCount int,
	perWorkerRPS int,
	logger *observability.Logger,
	now func() time.Time,
	autoStart bool,
) *RefreshWorkers {
	defaults := cache.DefaultPolicy()
	if perWorkerRPS <= 0 {
		perWorkerRPS = defaults.RevalidatePerWorkerRPS
	}
	if perWorkerRPS <= 0 {
		perWorkerRPS = 1
	}
	workerCount = normalizeWorkerCount(workerCount)
	if logger == nil {
		logger = observability.NewLogger(io.Discard)
	}
	if now == nil {
		now = time.Now
	}

	minInterval := time.Second / time.Duration(perWorkerRPS)
	if minInterval <= 0 {
		minInterval = time.Nanosecond
	}

	workers := &RefreshWorkers{
		queue:              queue,
		refresher:          refresher,
		logger:             logger,
		now:                now,
		minInterval:        minInterval,
		initialWorkerCount: workerCount,
		countUpdates:       make(chan int, 1),
		done:               make(chan struct{}),
	}

	if !autoStart {
		close(workers.done)
		return workers
	}

	ctx, cancel := context.WithCancel(context.Background())
	workers.cancel = cancel
	go workers.run(ctx)

	return workers
}

// SetWorkerCount updates the number of active refresh workers.
func (w *RefreshWorkers) SetWorkerCount(workerCount int) {
	if w == nil || w.countUpdates == nil {
		return
	}

	workerCount = normalizeWorkerCount(workerCount)

	select {
	case w.countUpdates <- workerCount:
	default:
		select {
		case <-w.countUpdates:
		default:
		}
		select {
		case w.countUpdates <- workerCount:
		default:
		}
	}
}

// SetMetrics attaches optional runtime metrics counters.
func (w *RefreshWorkers) SetMetrics(metrics *observability.Metrics) {
	if w == nil {
		return
	}
	w.metrics.Store(metrics)
}

// Close stops all refresh workers.
func (w *RefreshWorkers) Close() {
	if w == nil || w.cancel == nil {
		return
	}

	w.cancel()
	<-w.done
}

func (w *RefreshWorkers) run(ctx context.Context) {
	defer close(w.done)

	if w.queue == nil || w.refresher == nil {
		return
	}

	var wg sync.WaitGroup

	workerCancels := make([]context.CancelFunc, 0, w.initialWorkerCount)
	spawnWorker := func() {
		workerCtx, workerCancel := context.WithCancel(ctx)
		workerCancels = append(workerCancels, workerCancel)
		wg.Go(func() {
			w.runWorker(workerCtx)
		})
	}
	stopWorker := func() bool {
		if len(workerCancels) == 0 {
			return false
		}
		last := len(workerCancels) - 1
		cancel := workerCancels[last]
		workerCancels = workerCancels[:last]
		cancel()
		return true
	}
	scaleTo := func(workerCount int) {
		target := normalizeWorkerCount(workerCount)
		for len(workerCancels) < target {
			spawnWorker()
		}
		for len(workerCancels) > target {
			if !stopWorker() {
				return
			}
		}
	}

	scaleTo(w.initialWorkerCount)

	for {
		select {
		case <-ctx.Done():
			for stopWorker() {
			}
			wg.Wait()
			return
		case workerCount := <-w.countUpdates:
			scaleTo(workerCount)
		}
	}
}

func normalizeWorkerCount(workerCount int) int {
	if workerCount <= 0 {
		return defaultRefreshWorkerCount
	}
	return workerCount
}

func (w *RefreshWorkers) runWorker(ctx context.Context) {
	lastRunAt := time.Time{}

	for {
		key, ok := w.queue.Dequeue(ctx)
		if !ok {
			return
		}
		if key == "" {
			continue
		}

		if !lastRunAt.IsZero() {
			elapsed := w.now().Sub(lastRunAt)
			wait := w.minInterval - elapsed
			if wait > 0 {
				if !waitForInterval(ctx, wait) {
					return
				}
			}
		}

		_ = w.refresher.RefreshByKey(key)
		if metrics := w.metrics.Load(); metrics != nil {
			metrics.IncRefreshProcessed()
		}
		lastRunAt = w.now()
	}
}

func waitForInterval(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
