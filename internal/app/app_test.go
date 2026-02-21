package app

import (
	"testing"
	"time"
)

func TestDrainOnShutdownInvokesAllDrainers(t *testing.T) {
	t.Parallel()

	first := &fakeShutdownDrainer{}
	second := &fakeShutdownDrainer{}

	DrainOnShutdown(250*time.Millisecond, first, nil, second)

	if first.calls != 1 {
		t.Fatalf("expected first drainer to be called once, got %d", first.calls)
	}
	if second.calls != 1 {
		t.Fatalf("expected second drainer to be called once, got %d", second.calls)
	}
	if first.lastTimeout != 250*time.Millisecond {
		t.Fatalf("expected timeout 250ms, got %s", first.lastTimeout)
	}
	if second.lastTimeout != 250*time.Millisecond {
		t.Fatalf("expected timeout 250ms, got %s", second.lastTimeout)
	}
}

func TestDrainOnShutdownSkipsWhenTimeoutNonPositive(t *testing.T) {
	t.Parallel()

	drainer := &fakeShutdownDrainer{}

	DrainOnShutdown(0, drainer)
	DrainOnShutdown(-time.Second, drainer)

	if drainer.calls != 0 {
		t.Fatalf("expected no drain calls, got %d", drainer.calls)
	}
}

func TestCloseOnShutdownInvokesAllClosers(t *testing.T) {
	t.Parallel()

	first := &fakeShutdownCloser{}
	second := &fakeShutdownCloser{}

	CloseOnShutdown(first, nil, second)

	if first.calls != 1 {
		t.Fatalf("expected first closer to be called once, got %d", first.calls)
	}
	if second.calls != 1 {
		t.Fatalf("expected second closer to be called once, got %d", second.calls)
	}
}

type fakeShutdownDrainer struct {
	calls       int
	lastTimeout time.Duration
}

func (f *fakeShutdownDrainer) CloseWithDrain(timeout time.Duration) {
	f.calls++
	f.lastTimeout = timeout
}

type fakeShutdownCloser struct {
	calls int
}

func (f *fakeShutdownCloser) Close() {
	f.calls++
}
