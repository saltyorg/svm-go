package version

import (
	"context"
	"testing"
	"time"
)

func TestRefreshQueueEnqueueIsBoundedAndNonBlocking(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(1)
	defer queue.Close()

	if ok := queue.Enqueue("key-1"); !ok {
		t.Fatal("expected first enqueue to succeed")
	}

	start := time.Now()
	if ok := queue.Enqueue("key-2"); ok {
		t.Fatal("expected enqueue to drop when queue is full")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("expected non-blocking enqueue on full queue, took %s", elapsed)
	}
}

func TestRefreshQueueDequeueReturnsQueuedKey(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(1)
	defer queue.Close()
	if ok := queue.Enqueue("key-1"); !ok {
		t.Fatal("expected enqueue to succeed")
	}

	key, ok := queue.Dequeue(context.Background())
	if !ok {
		t.Fatal("expected dequeue to succeed")
	}
	if key != "key-1" {
		t.Fatalf("expected key %q, got %q", "key-1", key)
	}
}

func TestRefreshQueueDequeueReturnsOnCanceledContext(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(1)
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	if key, ok := queue.Dequeue(ctx); ok || key != "" {
		t.Fatalf("expected dequeue cancellation result, got key=%q ok=%t", key, ok)
	}
}

func TestRefreshQueueCloseStopsEnqueueAndDequeue(t *testing.T) {
	t.Parallel()

	queue := NewRefreshQueue(1)
	queue.Close()

	if ok := queue.Enqueue("key-1"); ok {
		t.Fatal("expected enqueue to fail after close")
	}
	if key, ok := queue.Dequeue(context.Background()); ok || key != "" {
		t.Fatalf("expected dequeue to stop after close, got key=%q ok=%t", key, ok)
	}
}
