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
	Enqueue(job RefreshJob) bool
}

type refreshWorkerScaler interface {
	SetWorkerCount(workerCount int)
}

// RevalidateSweepResult captures one scheduler sweep result.
type RevalidateSweepResult struct {
	CandidateKeys int
	DueKeys       int
	NotStaleKeys  int
	WorkerCount   int
	Enqueued      int
	Dropped       int
	SweepJobID    uint64
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
	jobTracker *RefreshJobTracker

	lastUpstreamRequests atomic.Uint64
	lastUpstreamErrors   atomic.Uint64
	lastRefreshUpdated   atomic.Uint64
	lastRefreshNotMod    atomic.Uint64
	lastRefreshSkipped   atomic.Uint64
	lastRefreshFailed    atomic.Uint64
	nextSweepJobID       atomic.Uint64

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

	go func() {
		scheduler.runWithFixedDelay(ctx)
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
	result := RevalidateSweepResult{
		CandidateKeys: len(keys),
	}
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
			result.NotStaleKeys++
			continue
		}
		dueKeys = append(dueKeys, key)
	}

	result.DueKeys = len(dueKeys)
	if s.jobTracker != nil && len(dueKeys) > 0 {
		result.SweepJobID = s.nextSweepJobID.Add(1)
		s.jobTracker.Start(result.SweepJobID, len(dueKeys))
	}
	result.WorkerCount = workerCountForDueEndpoints(len(dueKeys), s.policy.RevalidateEndpointsPerWorker)
	if s.scaler != nil {
		s.scaler.SetWorkerCount(result.WorkerCount)
	}

	for _, batch := range partitionRefreshKeys(dueKeys, s.policy.RevalidateEndpointsPerWorker) {
		for _, key := range batch {
			job := RefreshJob{
				ID:  result.SweepJobID,
				Key: key,
			}
			if s.queue.Enqueue(job) {
				result.Enqueued++
			} else {
				result.Dropped++
				if s.jobTracker != nil && result.SweepJobID != 0 {
					s.jobTracker.Complete(result.SweepJobID)
				}
			}
		}
	}

	if s.jobTracker != nil && result.SweepJobID != 0 {
		_ = s.jobTracker.Wait(ctx, result.SweepJobID)
	}

	out := s.finishSweep(result)
	if s.jobTracker != nil && result.SweepJobID != 0 {
		s.jobTracker.Cleanup(result.SweepJobID)
	}

	return out
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
			_ = s.Sweep(ctx)
		}
	}
}

func (s *RevalidateScheduler) runWithFixedDelay(ctx context.Context) {
	defer close(s.done)

	timer := time.NewTimer(s.interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = s.Sweep(ctx)
			timer.Reset(s.interval)
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
		s.lastRefreshUpdated.Store(0)
		s.lastRefreshNotMod.Store(0)
		s.lastRefreshSkipped.Store(0)
		s.lastRefreshFailed.Store(0)
		return
	}

	snapshot := metrics.Snapshot()
	s.lastUpstreamRequests.Store(snapshot.UpstreamRequests)
	s.lastUpstreamErrors.Store(snapshot.UpstreamErrors)
	s.lastRefreshUpdated.Store(snapshot.RefreshUpdated)
	s.lastRefreshNotMod.Store(snapshot.RefreshNotMod)
	s.lastRefreshSkipped.Store(snapshot.RefreshSkipped)
	s.lastRefreshFailed.Store(snapshot.RefreshFailed)
}

// SetWorkerScaler attaches an optional worker scaler used to adjust refresh-worker count per sweep.
func (s *RevalidateScheduler) SetWorkerScaler(scaler refreshWorkerScaler) {
	if s == nil {
		return
	}
	s.scaler = scaler
}

// SetJobTracker attaches optional per-sweep refresh completion tracking.
func (s *RevalidateScheduler) SetJobTracker(tracker *RefreshJobTracker) {
	if s == nil {
		return
	}
	s.jobTracker = tracker
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
		observability.Int("not_stale_keys", result.NotStaleKeys),
		observability.Int("worker_count", result.WorkerCount),
		observability.Int("enqueued", result.Enqueued),
		observability.Int("dropped", result.Dropped),
	}
	if result.SweepJobID != 0 {
		fields = append(fields, observability.Any("sweep_job_id", result.SweepJobID))
	}
	if s.metrics != nil {
		snapshot := s.metrics.Snapshot()
		upstreamRequestsDelta := counterDelta(s.lastUpstreamRequests.Swap(snapshot.UpstreamRequests), snapshot.UpstreamRequests)
		upstreamErrorsDelta := counterDelta(s.lastUpstreamErrors.Swap(snapshot.UpstreamErrors), snapshot.UpstreamErrors)
		refreshUpdatedDelta := counterDelta(s.lastRefreshUpdated.Swap(snapshot.RefreshUpdated), snapshot.RefreshUpdated)
		refreshNotModDelta := counterDelta(s.lastRefreshNotMod.Swap(snapshot.RefreshNotMod), snapshot.RefreshNotMod)
		refreshSkippedDelta := counterDelta(s.lastRefreshSkipped.Swap(snapshot.RefreshSkipped), snapshot.RefreshSkipped)
		refreshFailedDelta := counterDelta(s.lastRefreshFailed.Swap(snapshot.RefreshFailed), snapshot.RefreshFailed)
		fields = append(fields,
			observability.Any("cycle_upstream_requests", upstreamRequestsDelta),
			observability.Any("cycle_upstream_errors", upstreamErrorsDelta),
			observability.Any("cycle_refresh_updated", refreshUpdatedDelta),
			observability.Any("cycle_refresh_not_modified", refreshNotModDelta),
			observability.Any("cycle_refresh_skipped", refreshSkippedDelta),
			observability.Any("cycle_refresh_failed", refreshFailedDelta),
		)
		if snapshot.RateLimitRemain >= 0 {
			fields = append(fields, observability.Any("rate_limit_remaining", snapshot.RateLimitRemain))
		} else {
			fields = append(fields, observability.String("rate_limit_remaining", "unknown"))
		}
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
