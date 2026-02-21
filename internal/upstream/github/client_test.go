package github

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestClientGetSetsAuthorizationAndIfNoneMatch(t *testing.T) {
	t.Parallel()

	var gotAuthorization string
	var gotIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotAuthorization = request.Header.Get("Authorization")
		gotIfNoneMatch = request.Header.Get("If-None-Match")
	}))
	defer server.Close()

	client := NewClient()
	_, err := client.Get(context.Background(), server.URL, "token-a", `"etag-1"`)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if gotAuthorization != "token token-a" {
		t.Fatalf("expected authorization header %q, got %q", "token token-a", gotAuthorization)
	}
	if gotIfNoneMatch != `"etag-1"` {
		t.Fatalf("expected If-None-Match header %q, got %q", `"etag-1"`, gotIfNoneMatch)
	}
}

func TestClientGetOmitsIfNoneMatchWhenETagEmpty(t *testing.T) {
	t.Parallel()

	var gotIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotIfNoneMatch = request.Header.Get("If-None-Match")
	}))
	defer server.Close()

	client := NewClient()
	_, err := client.Get(context.Background(), server.URL, "token-a", "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if gotIfNoneMatch != "" {
		t.Fatalf("expected empty If-None-Match header, got %q", gotIfNoneMatch)
	}
}
