package observability

import "sync/atomic"

// Metrics stores low-cardinality in-memory counters for request and cache flows.
type Metrics struct {
	cacheHits        atomic.Uint64
	cacheMisses      atomic.Uint64
	revalidateRuns   atomic.Uint64
	upstreamRequests atomic.Uint64
	upstreamErrors   atomic.Uint64
}

// Snapshot is a point-in-time read of all metrics counters.
type Snapshot struct {
	CacheHits        uint64
	CacheMisses      uint64
	RevalidateRuns   uint64
	UpstreamRequests uint64
	UpstreamErrors   uint64
}

// NewMetrics constructs an empty counter set.
func NewMetrics() *Metrics {
	return &Metrics{}
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

// Snapshot returns an atomic read of all counters.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}

	return Snapshot{
		CacheHits:        m.cacheHits.Load(),
		CacheMisses:      m.cacheMisses.Load(),
		RevalidateRuns:   m.revalidateRuns.Load(),
		UpstreamRequests: m.upstreamRequests.Load(),
		UpstreamErrors:   m.upstreamErrors.Load(),
	}
}
