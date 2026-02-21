package github

import (
	"testing"
	"time"
)

func TestRoundRobinTokenProviderNextTokenCyclesDeterministically(t *testing.T) {
	t.Parallel()

	provider := NewRoundRobinTokenProvider([]string{"token-a", "token-b", "token-c"})

	sequence := []string{
		provider.NextToken(),
		provider.NextToken(),
		provider.NextToken(),
		provider.NextToken(),
		provider.NextToken(),
	}

	expected := []string{"token-a", "token-b", "token-c", "token-a", "token-b"}
	for i := range expected {
		if sequence[i] != expected[i] {
			t.Fatalf("expected sequence[%d]=%q, got %q", i, expected[i], sequence[i])
		}
	}
}

func TestRoundRobinTokenProviderSkipsEmptyTokens(t *testing.T) {
	t.Parallel()

	provider := NewRoundRobinTokenProvider([]string{"token-a", "", "token-b"})

	first := provider.NextToken()
	second := provider.NextToken()
	third := provider.NextToken()

	if first != "token-a" {
		t.Fatalf("expected first token to be token-a, got %q", first)
	}
	if second != "token-b" {
		t.Fatalf("expected second token to be token-b, got %q", second)
	}
	if third != "token-a" {
		t.Fatalf("expected third token to wrap to token-a, got %q", third)
	}
}

func TestRoundRobinTokenProviderReturnsEmptyWhenNoTokens(t *testing.T) {
	t.Parallel()

	provider := NewRoundRobinTokenProvider(nil)
	if token := provider.NextToken(); token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}

	var nilProvider *RoundRobinTokenProvider
	if token := nilProvider.NextToken(); token != "" {
		t.Fatalf("expected empty token for nil provider, got %q", token)
	}
}

func TestThresholdTokenProviderDeprioritizesLowRemainingTokens(t *testing.T) {
	t.Parallel()

	provider := NewThresholdTokenProvider([]string{"token-a", "token-b", "token-c"}, 50)
	provider.ObserveTokenRemaining("token-a", 10)
	provider.ObserveTokenRemaining("token-b", 20)
	provider.ObserveTokenRemaining("token-c", 75)

	for range 5 {
		if token := provider.NextToken(); token != "token-c" {
			t.Fatalf("expected token-c when alternatives are below threshold, got %q", token)
		}
	}
}

func TestThresholdTokenProviderFallsBackWhenAllTokensAreBelowThreshold(t *testing.T) {
	t.Parallel()

	provider := NewThresholdTokenProvider([]string{"token-a", "token-b"}, 50)
	provider.ObserveTokenRemaining("token-a", 10)
	provider.ObserveTokenRemaining("token-b", 20)

	got := []string{
		provider.NextToken(),
		provider.NextToken(),
		provider.NextToken(),
	}
	want := []string{"token-a", "token-b", "token-a"}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected sequence[%d]=%q, got %q", i, want[i], got[i])
		}
	}
}

func TestThresholdTokenProviderTreatsUnknownRemainingAsPreferred(t *testing.T) {
	t.Parallel()

	provider := NewThresholdTokenProvider([]string{"token-a", "token-b"}, 50)
	provider.ObserveTokenRemaining("token-a", 10)

	if token := provider.NextToken(); token != "token-b" {
		t.Fatalf("expected token-b because token-a is below threshold and token-b is unknown, got %q", token)
	}
}

func TestTokenProviderSkipsTokenDuringCooldown(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	provider := newTokenProvider(
		[]string{"token-a", "token-b"},
		0,
		30*time.Second,
		func() time.Time { return now },
	)
	provider.ObserveRateLimitedToken("token-a")

	if token := provider.NextToken(); token != "token-b" {
		t.Fatalf("expected token-b while token-a is in cooldown, got %q", token)
	}

	now = now.Add(31 * time.Second)
	got := []string{
		provider.NextToken(),
		provider.NextToken(),
	}
	want := []string{"token-b", "token-a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected sequence[%d]=%q after cooldown expiry, got %q", i, want[i], got[i])
		}
	}
}

func TestTokenProviderFallsBackWhenAllTokensAreInCooldown(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	provider := newTokenProvider(
		[]string{"token-a", "token-b"},
		0,
		time.Minute,
		func() time.Time { return now },
	)
	provider.ObserveRateLimitedToken("token-a")
	provider.ObserveRateLimitedToken("token-b")

	first := provider.NextToken()
	second := provider.NextToken()

	if first != "token-a" {
		t.Fatalf("expected fallback sequence first token-a, got %q", first)
	}
	if second != "token-b" {
		t.Fatalf("expected fallback sequence second token-b, got %q", second)
	}
}
