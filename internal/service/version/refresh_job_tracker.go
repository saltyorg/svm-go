package version

import (
	"context"
	"sync"
)

type refreshJobState struct {
	expected  int
	completed int
	done      chan struct{}
}

// RefreshJobTracker tracks completion of refresh jobs by job ID.
type RefreshJobTracker struct {
	mu   sync.Mutex
	jobs map[uint64]*refreshJobState
}

// NewRefreshJobTracker constructs an empty tracker.
func NewRefreshJobTracker() *RefreshJobTracker {
	return &RefreshJobTracker{
		jobs: make(map[uint64]*refreshJobState),
	}
}

// Start registers a tracked job with an expected completion count.
func (t *RefreshJobTracker) Start(jobID uint64, expected int) {
	if t == nil || jobID == 0 || expected <= 0 {
		return
	}

	t.mu.Lock()
	t.jobs[jobID] = &refreshJobState{
		expected: expected,
		done:     make(chan struct{}),
	}
	t.mu.Unlock()
}

// Complete marks one completed unit for a tracked job.
func (t *RefreshJobTracker) Complete(jobID uint64) {
	if t == nil || jobID == 0 {
		return
	}

	t.mu.Lock()
	state, ok := t.jobs[jobID]
	if !ok {
		t.mu.Unlock()
		return
	}

	if state.completed >= state.expected {
		t.mu.Unlock()
		return
	}
	state.completed++
	if state.completed == state.expected {
		close(state.done)
	}
	t.mu.Unlock()
}

// Wait blocks until the tracked job reaches expected completion or context cancel.
func (t *RefreshJobTracker) Wait(ctx context.Context, jobID uint64) bool {
	if t == nil || jobID == 0 {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}

	t.mu.Lock()
	state, ok := t.jobs[jobID]
	t.mu.Unlock()
	if !ok {
		return true
	}

	select {
	case <-ctx.Done():
		return false
	case <-state.done:
		return true
	}
}

// Cleanup removes tracked job state.
func (t *RefreshJobTracker) Cleanup(jobID uint64) {
	if t == nil || jobID == 0 {
		return
	}

	t.mu.Lock()
	delete(t.jobs, jobID)
	t.mu.Unlock()
}
