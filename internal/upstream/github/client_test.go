package github

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

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

func TestClientGetDoesNotSendAuthorizationToUntrustedHost(t *testing.T) {
	t.Parallel()

	var gotAuthorization string
	var gotIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotAuthorization = request.Header.Get("Authorization")
		gotIfNoneMatch = request.Header.Get("If-None-Match")
	}))
	defer server.Close()

	client := NewClientWithHTTPClient(server.Client())
	_, err := client.Get(context.Background(), server.URL, "token-a", `"etag-1"`)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if gotAuthorization != "" {
		t.Fatalf("expected authorization header to be omitted, got %q", gotAuthorization)
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

	client := NewClientWithHTTPClient(server.Client())
	_, err := client.Get(context.Background(), server.URL, "token-a", "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if gotIfNoneMatch != "" {
		t.Fatalf("expected empty If-None-Match header, got %q", gotIfNoneMatch)
	}
}

func TestShouldAuthenticateOnlyGitHubAPIHost(t *testing.T) {
	t.Parallel()

	if !shouldAuthenticate(mustParseURL(t, "https://api.github.com/repos/org/repo")) {
		t.Fatal("expected GitHub API host to receive authentication")
	}
	if shouldAuthenticate(mustParseURL(t, "https://raw.githubusercontent.com/org/repo/main/file")) {
		t.Fatal("expected content host not to receive authentication")
	}
	if shouldAuthenticate(mustParseURL(t, "https://example.com")) {
		t.Fatal("expected custom host not to receive authentication")
	}
}

func TestClientSendsAuthorizationToGitHubAPI(t *testing.T) {
	t.Parallel()

	var authorization string
	client := NewClientWithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			authorization = request.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}),
	})
	response, err := client.Get(
		context.Background(),
		"https://api.github.com/repos/org/repo",
		"token-a",
		"",
	)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	_ = response.Body.Close()
	if authorization != "token token-a" {
		t.Fatalf("expected GitHub authorization header, got %q", authorization)
	}
}

func TestIsPublicIPRejectsPrivateAndSpecialAddresses(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"127.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"169.254.1.1",
		"192.0.2.1",
		"198.51.100.1",
		"203.0.113.1",
		"::1",
		"fc00::1",
		"2001:db8::1",
	} {
		if isPublicIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s to be rejected", raw)
		}
	}
	if !isPublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("expected public address to be allowed")
	}
}
