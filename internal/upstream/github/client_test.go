package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}

func TestClientRejectsRedirectOutsideAllowlist(t *testing.T) {
	t.Parallel()

	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer destination.Close()

	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	httpClient := source.Client()
	httpClient.CheckRedirect = redirectValidator([]string{"allowed.example"})
	client := NewClientWithHTTPClient(httpClient)
	response, err := client.Get(context.Background(), source.URL, "token-a", "")
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, ErrRedirectNotAllowed) {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}

func TestRedirectValidatorRejectsPlainHTTP(t *testing.T) {
	t.Parallel()

	err := redirectValidator([]string{"api.github.com"})(
		&http.Request{URL: mustParseURL(t, "http://api.github.com/path")},
		nil,
	)
	if !errors.Is(err, ErrRedirectNotAllowed) {
		t.Fatalf("expected plain HTTP redirect rejection, got %v", err)
	}
}

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
