package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"svm/internal/cache"
)

func TestLoadParsesAllowedUpstreamHosts(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALLOWED_UPSTREAM_HOSTS", " api.github.com,downloads.github.com ,,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	want := []string{"api.github.com", "downloads.github.com"}
	if !reflect.DeepEqual(cfg.AllowedUpstreamHosts, want) {
		t.Fatalf("expected allowed hosts %v, got %v", want, cfg.AllowedUpstreamHosts)
	}
}

func TestLoadUsesDefaultAllowedUpstreamHostsWhenUnset(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALLOWED_UPSTREAM_HOSTS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if !reflect.DeepEqual(cfg.AllowedUpstreamHosts, defaultAllowedUpstreamHosts) {
		t.Fatalf("expected default allowed hosts %v, got %v", defaultAllowedUpstreamHosts, cfg.AllowedUpstreamHosts)
	}
}

func TestLoadTrimsAndDeduplicatesTokens(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITHUB_PATS", " token-a,token-b, token-a ,,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	want := []string{"token-a", "token-b"}
	if !reflect.DeepEqual(cfg.GitHubPATs, want) {
		t.Fatalf("expected tokens %v, got %v", want, cfg.GitHubPATs)
	}
}

func TestLoadRejectsNoUsableTokens(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITHUB_PATS", " , , ")
	if _, err := Load(); err == nil {
		t.Fatal("expected unusable token list error")
	}
}

func TestLoadRejectsInvalidPortAndNegativeThreshold(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid port error")
	}

	setRequiredEnv(t)
	t.Setenv("API_USAGE_THRESHOLD", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected negative threshold error")
	}
}

func TestLoadRejectsExplicitlyEmptyAllowedUpstreamHosts(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALLOWED_UPSTREAM_HOSTS", " , , ")

	if _, err := Load(); err == nil {
		t.Fatal("expected empty allowlist error")
	}
}

func TestLoadAppliesDefaultCachePolicy(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	want := cache.DefaultPolicy()
	if !reflect.DeepEqual(cfg.CachePolicy, want) {
		t.Fatalf("expected default cache policy %+v, got %+v", want, cfg.CachePolicy)
	}
}

func TestLoadParsesCachePolicyOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CACHE_HARD_TTL", "2h")
	t.Setenv("CACHE_NEGATIVE_TTL", "15m")
	t.Setenv("REVALIDATE_INTERVAL", "90s")
	t.Setenv("REVALIDATE_LOOKBACK", "14d")
	t.Setenv("REVALIDATE_WORKERS", "3")
	t.Setenv("REVALIDATE_PER_WORKER_RPS", "2")
	t.Setenv("REFRESH_QUEUE_SIZE", "128")
	t.Setenv("CACHE_L1_MAX_GB", "2.5")
	t.Setenv("MAX_UPSTREAM_RESPONSE_BYTES", "2048")
	t.Setenv("SHUTDOWN_TIMEOUT", "4s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	got := cfg.CachePolicy
	if got.HardTTL != 2*time.Hour {
		t.Fatalf("expected HardTTL 2h, got %s", got.HardTTL)
	}
	if got.NegativeTTL != 15*time.Minute {
		t.Fatalf("expected NegativeTTL 15m, got %s", got.NegativeTTL)
	}
	if got.RevalidateInterval != 90*time.Second {
		t.Fatalf("expected RevalidateInterval 90s, got %s", got.RevalidateInterval)
	}
	if got.RevalidateLookback != 14*24*time.Hour {
		t.Fatalf("expected RevalidateLookback 336h, got %s", got.RevalidateLookback)
	}
	if got.MaxUpstreamResponseBytes != 2048 {
		t.Fatalf("expected MaxUpstreamResponseBytes 2048, got %d", got.MaxUpstreamResponseBytes)
	}
	if got.RevalidateWorkers != 3 {
		t.Fatalf("expected RevalidateWorkers 3, got %d", got.RevalidateWorkers)
	}
	if got.RevalidatePerWorkerRPS != 2 {
		t.Fatalf("expected RevalidatePerWorkerRPS 2, got %d", got.RevalidatePerWorkerRPS)
	}
	if got.RefreshQueueSize != 128 {
		t.Fatalf("expected RefreshQueueSize 128, got %d", got.RefreshQueueSize)
	}
	if got.L1MaxGB != 2.5 {
		t.Fatalf("expected L1MaxGB 2.5, got %v", got.L1MaxGB)
	}
	if got.ShutdownTimeout != 4*time.Second {
		t.Fatalf("expected ShutdownTimeout 4s, got %s", got.ShutdownTimeout)
	}
}

func TestLoadRejectsInvalidCachePolicyOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CACHE_HARD_TTL", "nonsense")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected an error for invalid CACHE_HARD_TTL")
	}
	if !strings.Contains(err.Error(), "CACHE_HARD_TTL") {
		t.Fatalf("expected error to mention CACHE_HARD_TTL, got %v", err)
	}
}

func TestLoadRejectsMissingRequiredEnvironmentVariables(t *testing.T) {
	t.Setenv("GITHUB_PATS", "")
	t.Setenv("API_USAGE_THRESHOLD", "50")

	_, err := Load()
	if err == nil {
		t.Fatal("expected missing required env var error")
	}
	if !strings.Contains(err.Error(), "GITHUB_PATS") {
		t.Fatalf("expected missing GITHUB_PATS in error, got %v", err)
	}
}

func TestLoadUsesDefaultPortWhenUnset(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PORT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if cfg.Port != "8000" {
		t.Fatalf("expected default port 8000, got %q", cfg.Port)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_PATS", "pat-1,pat-2")
	t.Setenv("API_USAGE_THRESHOLD", "50")
}
