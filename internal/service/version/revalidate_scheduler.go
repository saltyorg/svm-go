package version

import (
	"context"
	"io"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
)

type activeKeySource interface {
	ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error)
}

type staleActiveKeyPruner interface {
	PruneActiveKeysBefore(ctx context.Context, cutoff time.Time) error
}

type refreshEnqueuer interface {
	Enqueue(job RefreshJob) bool
}

// RevalidateSweepResult captures one scheduler sweep result.
type RevalidateSweepResult struct {
	CandidateKeys int
	DueKeys       int
	NotStaleKeys  int
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
	if policy.RevalidateWorkers <= 0 {
		policy.RevalidateWorkers = defaults.RevalidateWorkers
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
	if pruner, ok := s.activeKeys.(staleActiveKeyPruner); ok {
		if err := pruner.PruneActiveKeysBefore(ctx, since); err != nil {
			s.logger.Warn("failed to prune inactive keys", observability.String("error", err.Error()))
		}
	}
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
	result.WorkerCount = s.policy.RevalidateWorkers
	for _, key := range dueKeys {
		if s.queue.Enqueue(RefreshJob{Key: key}) {
			result.Enqueued++
		} else {
			result.Dropped++
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
}

func (s *RevalidateScheduler) incRevalidateRun() {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.IncRevalidateRun()
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
	if s.metrics != nil {
		snapshot := s.metrics.Snapshot()
		if snapshot.RateLimitRemain >= 0 {
			fields = append(fields, observability.Any("rate_limit_remaining", snapshot.RateLimitRemain))
		} else {
			fields = append(fields, observability.String("rate_limit_remaining", "unknown"))
		}
	}

	s.logger.Info("revalidation sweep complete", fields...)
	return result
}
