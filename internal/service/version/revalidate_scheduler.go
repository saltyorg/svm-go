package version

import (
	"context"
	"io"
	"sync/atomic"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
)

type activeKeySource interface {
	ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error)
}

type refreshEnqueuer interface {
	Enqueue(key string) bool
}

type refreshWorkerScaler interface {
	SetWorkerCount(workerCount int)
}

// RevalidateSweepResult captures one scheduler sweep result.
type RevalidateSweepResult struct {
	CandidateKeys int
	DueKeys       int
	WorkerCount   int
	Enqueued      int
	Dropped       int
}

// RevalidateScheduler periodically selects due active keys and enqueues refresh work.
type RevalidateScheduler struct {
	activeKeys activeKeySource
	cacheStore CacheStore
	queue      refreshEnqueuer
	policy     cache.Policy
	logger     *observability.Logger
	metrics    *observability.Metrics
	now        func() time.Time
	interval   time.Duration
	scaler     refreshWorkerScaler

	lastUpstreamRequests atomic.Uint64
	lastUpstreamErrors   atomic.Uint64

	cancel context.CancelFunc
	done   chan struct{}
}

// NewRevalidateScheduler creates and starts a scheduler worker.
func NewRevalidateScheduler(
	activeKeys activeKeySource,
	cacheStore CacheStore,
	queue refreshEnqueuer,
	policy cache.Policy,
	logger *observability.Logger,
) *RevalidateScheduler {
	return newRevalidateScheduler(
		activeKeys,
		cacheStore,
		queue,
		policy,
		logger,
		time.Now,
		true,
		nil,
	)
}

func newRevalidateScheduler(
	activeKeys activeKeySource,
	cacheStore CacheStore,
	queue refreshEnqueuer,
	policy cache.Policy,
	logger *observability.Logger,
	now func() time.Time,
	autoStart bool,
	tickCh <-chan time.Time,
) *RevalidateScheduler {
	defaults := cache.DefaultPolicy()
	if policy.RevalidateInterval <= 0 {
		policy.RevalidateInterval = defaults.RevalidateInterval
	}
	if policy.RevalidateLookback <= 0 {
		policy.RevalidateLookback = defaults.RevalidateLookback
	}
	if policy.RevalidateEndpointsPerWorker <= 0 {
		policy.RevalidateEndpointsPerWorker = defaults.RevalidateEndpointsPerWorker
	}
	if logger == nil {
		logger = observability.NewLogger(io.Discard)
	}
	if now == nil {
		now = time.Now
	}

	scheduler := &RevalidateScheduler{
		activeKeys: activeKeys,
		cacheStore: cacheStore,
		queue:      queue,
		policy:     policy,
		logger:     logger,
		now:        now,
		interval:   policy.RevalidateInterval,
		done:       make(chan struct{}),
	}

	if !autoStart {
		close(scheduler.done)
		return scheduler
	}

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.cancel = cancel

	if tickCh != nil {
		go scheduler.run(ctx, tickCh)
		return scheduler
	}

	ticker := time.NewTicker(scheduler.interval)
	go func() {
		defer ticker.Stop()
		scheduler.run(ctx, ticker.C)
	}()

	return scheduler
}

// Sweep selects due active keys and enqueues them for async refresh.
func (s *RevalidateScheduler) Sweep(ctx context.Context) RevalidateSweepResult {
	if s == nil {
		return RevalidateSweepResult{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.incRevalidateRun()
	if s.activeKeys == nil || s.cacheStore == nil || s.queue == nil {
		return s.finishSweep(RevalidateSweepResult{})
	}

	now := s.now().UTC()
	since := now.Add(-s.policy.RevalidateLookback)
	keys, err := s.activeKeys.ActiveKeysSince(ctx, since, 0)
	if err != nil {
		s.logger.Warn(
			"failed to list active keys for revalidation",
			observability.String("error", err.Error()),
		)
		return s.finishSweep(RevalidateSweepResult{})
	}

	dueKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}

		record, hit, getErr := s.cacheStore.Get(ctx, key)
		if getErr != nil {
			s.logger.Warn(
				"failed to load cache record for revalidation",
				observability.String("key", key),
				observability.String("error", getErr.Error()),
			)
			continue
		}
		if !hit {
			continue
		}
		if !cache.IsRevalidationDue(record.LastCheckedAt, now, s.policy.RevalidateInterval) {
			continue
		}
		dueKeys = append(dueKeys, key)
	}

	result := RevalidateSweepResult{
		CandidateKeys: len(keys),
		DueKeys:       len(dueKeys),
		WorkerCount:   workerCountForDueEndpoints(len(dueKeys), s.policy.RevalidateEndpointsPerWorker),
	}
	if s.scaler != nil {
		s.scaler.SetWorkerCount(result.WorkerCount)
	}

	for _, batch := range partitionRefreshKeys(dueKeys, s.policy.RevalidateEndpointsPerWorker) {
		for _, key := range batch {
			if s.queue.Enqueue(key) {
				result.Enqueued++
			} else {
				result.Dropped++
			}
		}
	}

	return s.finishSweep(result)
}

// Close stops the scheduler worker.
func (s *RevalidateScheduler) Close() {
	if s == nil || s.cancel == nil {
		return
	}

	s.cancel()
	<-s.done
}

func (s *RevalidateScheduler) run(ctx context.Context, tickCh <-chan time.Time) {
	defer close(s.done)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tickCh:
			_ = s.Sweep(context.Background())
		}
	}
}

// SetMetrics attaches optional runtime metrics counters.
func (s *RevalidateScheduler) SetMetrics(metrics *observability.Metrics) {
	if s == nil {
		return
	}
	s.metrics = metrics
	if metrics == nil {
		s.lastUpstreamRequests.Store(0)
		s.lastUpstreamErrors.Store(0)
		return
	}

	snapshot := metrics.Snapshot()
	s.lastUpstreamRequests.Store(snapshot.UpstreamRequests)
	s.lastUpstreamErrors.Store(snapshot.UpstreamErrors)
}

// SetWorkerScaler attaches an optional worker scaler used to adjust refresh-worker count per sweep.
func (s *RevalidateScheduler) SetWorkerScaler(scaler refreshWorkerScaler) {
	if s == nil {
		return
	}
	s.scaler = scaler
}

func (s *RevalidateScheduler) incRevalidateRun() {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.IncRevalidateRun()
}

func workerCountForDueEndpoints(dueEndpoints, perWorker int) int {
	if perWorker <= 0 {
		perWorker = cache.DefaultPolicy().RevalidateEndpointsPerWorker
	}
	if dueEndpoints <= 0 {
		return 1
	}

	return (dueEndpoints + perWorker - 1) / perWorker
}

func partitionRefreshKeys(keys []string, perWorker int) [][]string {
	if len(keys) == 0 {
		return nil
	}
	if perWorker <= 0 {
		perWorker = cache.DefaultPolicy().RevalidateEndpointsPerWorker
	}

	partitions := make([][]string, 0, workerCountForDueEndpoints(len(keys), perWorker))
	for start := 0; start < len(keys); start += perWorker {
		end := min(start+perWorker, len(keys))

		batch := append([]string(nil), keys[start:end]...)
		partitions = append(partitions, batch)
	}

	return partitions
}

func (s *RevalidateScheduler) finishSweep(result RevalidateSweepResult) RevalidateSweepResult {
	if s == nil || s.logger == nil {
		return result
	}

	fields := []observability.Field{
		observability.Int("candidate_keys", result.CandidateKeys),
		observability.Int("due_keys", result.DueKeys),
		observability.Int("worker_count", result.WorkerCount),
		observability.Int("enqueued", result.Enqueued),
		observability.Int("dropped", result.Dropped),
	}
	if s.metrics != nil {
		snapshot := s.metrics.Snapshot()
		upstreamRequestsDelta := counterDelta(s.lastUpstreamRequests.Swap(snapshot.UpstreamRequests), snapshot.UpstreamRequests)
		upstreamErrorsDelta := counterDelta(s.lastUpstreamErrors.Swap(snapshot.UpstreamErrors), snapshot.UpstreamErrors)
		fields = append(fields,
			observability.Any("cycle_upstream_requests", upstreamRequestsDelta),
			observability.Any("cycle_upstream_errors", upstreamErrorsDelta),
		)
	}

	s.logger.Info("revalidation sweep complete", fields...)
	return result
}

func counterDelta(previous, current uint64) uint64 {
	if current < previous {
		return current
	}

	return current - previous
}
