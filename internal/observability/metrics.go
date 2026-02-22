package observability

import "sync/atomic"

const unknownRateLimitRemaining int64 = -1

// Metrics stores low-cardinality in-memory counters for request and cache flows.
type Metrics struct {
	cacheHits        atomic.Uint64
	cacheMisses      atomic.Uint64
	revalidateRuns   atomic.Uint64
	upstreamRequests atomic.Uint64
	upstreamErrors   atomic.Uint64
	refreshUpdated   atomic.Uint64
	refreshNotMod    atomic.Uint64
	refreshSkipped   atomic.Uint64
	refreshFailed    atomic.Uint64
	refreshProcessed atomic.Uint64
	rateLimitRemain  atomic.Int64
}

// Snapshot is a point-in-time read of all metrics counters.
type Snapshot struct {
	CacheHits        uint64
	CacheMisses      uint64
	RevalidateRuns   uint64
	UpstreamRequests uint64
	UpstreamErrors   uint64
	RefreshUpdated   uint64
	RefreshNotMod    uint64
	RefreshSkipped   uint64
	RefreshFailed    uint64
	RefreshProcessed uint64
	RateLimitRemain  int64
}

// NewMetrics constructs an empty counter set.
func NewMetrics() *Metrics {
	metrics := &Metrics{}
	metrics.rateLimitRemain.Store(unknownRateLimitRemaining)
	return metrics
}

// IncCacheHit increments cache-hit counter.
func (m *Metrics) IncCacheHit() {
	if m == nil {
		return
	}
	m.cacheHits.Add(1)
}

// IncCacheMiss increments cache-miss counter.
func (m *Metrics) IncCacheMiss() {
	if m == nil {
		return
	}
	m.cacheMisses.Add(1)
}

// IncRevalidateRun increments background revalidation counter.
func (m *Metrics) IncRevalidateRun() {
	if m == nil {
		return
	}
	m.revalidateRuns.Add(1)
}

// IncUpstreamRequest increments total upstream-request counter.
func (m *Metrics) IncUpstreamRequest() {
	if m == nil {
		return
	}
	m.upstreamRequests.Add(1)
}

// IncUpstreamError increments upstream-error counter.
func (m *Metrics) IncUpstreamError() {
	if m == nil {
		return
	}
	m.upstreamErrors.Add(1)
}

// IncRefreshUpdated increments background-refresh updated counter.
func (m *Metrics) IncRefreshUpdated() {
	if m == nil {
		return
	}
	m.refreshUpdated.Add(1)
}

// IncRefreshNotMod increments background-refresh not-modified counter.
func (m *Metrics) IncRefreshNotMod() {
	if m == nil {
		return
	}
	m.refreshNotMod.Add(1)
}

// IncRefreshSkipped increments background-refresh skipped counter.
func (m *Metrics) IncRefreshSkipped() {
	if m == nil {
		return
	}
	m.refreshSkipped.Add(1)
}

// IncRefreshFailed increments background-refresh failed counter.
func (m *Metrics) IncRefreshFailed() {
	if m == nil {
		return
	}
	m.refreshFailed.Add(1)
}

// IncRefreshProcessed increments background-refresh processed counter.
func (m *Metrics) IncRefreshProcessed() {
	if m == nil {
		return
	}
	m.refreshProcessed.Add(1)
}

// ObserveRateLimitRemaining stores the latest observed upstream rate-limit remaining value.
func (m *Metrics) ObserveRateLimitRemaining(remaining int) {
	if m == nil {
		return
	}
	if remaining < 0 {
		remaining = 0
	}

	m.rateLimitRemain.Store(int64(remaining))
}

// Snapshot returns an atomic read of all counters.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{
			RateLimitRemain: unknownRateLimitRemaining,
		}
	}

	return Snapshot{
		CacheHits:        m.cacheHits.Load(),
		CacheMisses:      m.cacheMisses.Load(),
		RevalidateRuns:   m.revalidateRuns.Load(),
		UpstreamRequests: m.upstreamRequests.Load(),
		UpstreamErrors:   m.upstreamErrors.Load(),
		RefreshUpdated:   m.refreshUpdated.Load(),
		RefreshNotMod:    m.refreshNotMod.Load(),
		RefreshSkipped:   m.refreshSkipped.Load(),
		RefreshFailed:    m.refreshFailed.Load(),
		RefreshProcessed: m.refreshProcessed.Load(),
		RateLimitRemain:  m.rateLimitRemain.Load(),
	}
}
