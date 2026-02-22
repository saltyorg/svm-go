package version

import (
	"context"
	"testing"
	"time"
)

func TestRefreshJobTrackerWaitReturnsWhenJobCompletes(t *testing.T) {
	t.Parallel()

	tracker := NewRefreshJobTracker()
	tracker.Start(1, 2)

	done := make(chan bool, 1)
	go func() {
		done <- tracker.Wait(context.Background(), 1)
	}()

	tracker.Complete(1)
	tracker.Complete(1)

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("expected wait to return true on completion")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion")
	}
}

func TestRefreshJobTrackerWaitReturnsFalseOnContextCancel(t *testing.T) {
	t.Parallel()

	tracker := NewRefreshJobTracker()
	tracker.Start(2, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if ok := tracker.Wait(ctx, 2); ok {
		t.Fatal("expected wait to return false on context cancellation")
	}
}

func TestRefreshJobTrackerCleanupRemovesJob(t *testing.T) {
	t.Parallel()

	tracker := NewRefreshJobTracker()
	tracker.Start(3, 1)
	tracker.Cleanup(3)

	tracker.mu.Lock()
	_, exists := tracker.jobs[3]
	tracker.mu.Unlock()
	if exists {
		t.Fatal("expected job to be removed after cleanup")
	}
}
