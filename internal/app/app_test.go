package app

import (
	"testing"
)

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

type fakeShutdownCloser struct {
	calls int
}

func (f *fakeShutdownCloser) Close() {
	f.calls++
}
