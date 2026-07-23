package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"svm/internal/cache"
)

const (
	defaultPort = "8000"
)

var defaultAllowedUpstreamHosts = []string{
	"api.github.com",
	"uploads.github.com",
	"raw.githubusercontent.com",
	"codeload.github.com",
	"objects.githubusercontent.com",
	"github-releases.githubusercontent.com",
}

// Config holds runtime configuration parsed from environment variables.
type Config struct {
	GitHubPATs           []string
	APIUsageThreshold    int
	ForwardedAllowIPs    string
	AllowedUpstreamHosts []string
	CachePolicy          cache.Policy
	Port                 string
}

// Load parses environment variables into a typed configuration object.
func Load() (Config, error) {
	cfg := Config{}

	requiredVars := []string{"GITHUB_PATS", "API_USAGE_THRESHOLD"}
	for _, variable := range requiredVars {
		if os.Getenv(variable) == "" {
			return Config{}, fmt.Errorf("%s not set in environment variables", variable)
		}
	}

	cfg.GitHubPATs = parseCSVTrimDeduplicated(os.Getenv("GITHUB_PATS"))
	if len(cfg.GitHubPATs) == 0 {
		return Config{}, fmt.Errorf("GITHUB_PATS must contain at least one token")
	}
	threshold, err := strconv.Atoi(strings.TrimSpace(os.Getenv("API_USAGE_THRESHOLD")))
	if err != nil {
		return Config{}, fmt.Errorf("invalid API_USAGE_THRESHOLD: %v", err)
	}
	if threshold < 0 {
		return Config{}, fmt.Errorf("API_USAGE_THRESHOLD must be zero or greater")
	}
	cfg.APIUsageThreshold = threshold
	cfg.ForwardedAllowIPs = os.Getenv("FORWARDED_ALLOW_IPS")
	cfg.AllowedUpstreamHosts = loadAllowedUpstreamHosts()
	if len(cfg.AllowedUpstreamHosts) == 0 {
		return Config{}, fmt.Errorf("ALLOWED_UPSTREAM_HOSTS must contain at least one host")
	}
	cfg.CachePolicy, err = loadCachePolicy()
	if err != nil {
		return Config{}, err
	}
	cfg.Port = strings.TrimSpace(os.Getenv("PORT"))
	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if err := validatePort(cfg.Port); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func loadAllowedUpstreamHosts() []string {
	raw := strings.TrimSpace(os.Getenv("ALLOWED_UPSTREAM_HOSTS"))
	if raw == "" {
		return append([]string(nil), defaultAllowedUpstreamHosts...)
	}

	return parseCSVLowerTrim(raw)
}

func parseCSVLowerTrim(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}

	return out
}

func parseCSVTrimDeduplicated(value string) []string {
	parts := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func validatePort(raw string) error {
	port, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("PORT must be an integer between 1 and 65535")
	}
	return nil
}

func loadCachePolicy() (cache.Policy, error) {
	policy := cache.DefaultPolicy()
	var err error

	policy.HardTTL, err = parseEnvDuration("CACHE_HARD_TTL", policy.HardTTL)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.NegativeTTL, err = parseEnvDuration("CACHE_NEGATIVE_TTL", policy.NegativeTTL)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.RevalidateInterval, err = parseEnvDuration("REVALIDATE_INTERVAL", policy.RevalidateInterval)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.RevalidateLookback, err = parseEnvDuration("REVALIDATE_LOOKBACK", policy.RevalidateLookback)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.RevalidateWorkers, err = parseEnvPositiveInt("REVALIDATE_WORKERS", policy.RevalidateWorkers)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.RevalidatePerWorkerRPS, err = parseEnvPositiveInt(
		"REVALIDATE_PER_WORKER_RPS",
		policy.RevalidatePerWorkerRPS,
	)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.RefreshQueueSize, err = parseEnvPositiveInt("REFRESH_QUEUE_SIZE", policy.RefreshQueueSize)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.L1MaxGB, err = parseEnvPositiveFloat("CACHE_L1_MAX_GB", policy.L1MaxGB)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.MaxUpstreamResponseBytes, err = parseEnvPositiveInt64(
		"MAX_UPSTREAM_RESPONSE_BYTES",
		policy.MaxUpstreamResponseBytes,
	)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.ShutdownTimeout, err = parseEnvDuration("SHUTDOWN_TIMEOUT", policy.ShutdownTimeout)
	if err != nil {
		return cache.Policy{}, err
	}

	return policy, nil
}

func parseEnvPositiveInt64(name string, defaultValue int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %v", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return value, nil
}

func parseEnvDuration(name string, defaultValue time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}

	duration, err := parseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %v", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}

	return duration, nil
}

func parseDuration(raw string) (time.Duration, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if before, ok := strings.CutSuffix(value, "d"); ok {
		daysValue := strings.TrimSpace(before)
		days, err := strconv.ParseFloat(daysValue, 64)
		if err != nil {
			return 0, fmt.Errorf("unsupported duration value %q", raw)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}

	return time.ParseDuration(value)
}

func parseEnvPositiveInt(name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %v", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}

	return value, nil
}

func parseEnvPositiveFloat(name string, defaultValue float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %v", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}

	return value, nil
}
