package github

import (
	"net/http"
	"testing"
)

type testCooldownObserver struct {
	calls int
	token string
}

func (o *testCooldownObserver) ObserveRateLimitedToken(token string) {
	o.calls++
	o.token = token
}

func TestClientObserveRateLimitMarksTokenOnForbidden(t *testing.T) {
	t.Parallel()

	client := NewClient()
	observer := &testCooldownObserver{}

	client.ObserveRateLimit("token-a", &http.Response{StatusCode: http.StatusForbidden}, observer)

	if observer.calls != 1 {
		t.Fatalf("expected observer call count 1, got %d", observer.calls)
	}
	if observer.token != "token-a" {
		t.Fatalf("expected observer token %q, got %q", "token-a", observer.token)
	}
}

func TestClientObserveRateLimitMarksTokenOnTooManyRequests(t *testing.T) {
	t.Parallel()

	client := NewClient()
	observer := &testCooldownObserver{}

	client.ObserveRateLimit("token-a", &http.Response{StatusCode: http.StatusTooManyRequests}, observer)

	if observer.calls != 1 {
		t.Fatalf("expected observer call count 1, got %d", observer.calls)
	}
}

func TestClientObserveRateLimitIgnoresNonRateLimitedStatuses(t *testing.T) {
	t.Parallel()

	client := NewClient()
	observer := &testCooldownObserver{}

	client.ObserveRateLimit("token-a", &http.Response{StatusCode: http.StatusInternalServerError}, observer)
	client.ObserveRateLimit("token-a", &http.Response{StatusCode: http.StatusOK}, observer)

	if observer.calls != 0 {
		t.Fatalf("expected observer call count 0, got %d", observer.calls)
	}
}

func TestBuildRequestHeadersIncludesAuthorizationAndIfNoneMatch(t *testing.T) {
	t.Parallel()

	headers := BuildRequestHeaders("token-a", `"etag-1"`)

	if headers["Authorization"] != "token token-a" {
		t.Fatalf("expected authorization header %q, got %q", "token token-a", headers["Authorization"])
	}
	if headers["If-None-Match"] != `"etag-1"` {
		t.Fatalf("expected If-None-Match header %q, got %q", `"etag-1"`, headers["If-None-Match"])
	}
}

func TestBuildRequestHeadersOmitsIfNoneMatchWhenETagEmpty(t *testing.T) {
	t.Parallel()

	headers := BuildRequestHeaders("token-a", "")

	if _, ok := headers["If-None-Match"]; ok {
		t.Fatal("expected If-None-Match header to be omitted when etag is empty")
	}
}
