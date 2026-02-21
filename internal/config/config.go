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
	RedisHost            string
	ForwardedAllowIPs    string
	AllowedUpstreamHosts []string
	CachePolicy          cache.Policy
	Port                 string
}

// Load parses environment variables into a typed configuration object.
func Load() (Config, error) {
	cfg := Config{}

	requiredVars := []string{"GITHUB_PATS", "API_USAGE_THRESHOLD", "REDIS_HOST"}
	for _, variable := range requiredVars {
		if os.Getenv(variable) == "" {
			return Config{}, fmt.Errorf("%s not set in environment variables", variable)
		}
	}

	cfg.GitHubPATs = strings.Split(os.Getenv("GITHUB_PATS"), ",")
	threshold, err := strconv.Atoi(os.Getenv("API_USAGE_THRESHOLD"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid API_USAGE_THRESHOLD: %v", err)
	}
	cfg.APIUsageThreshold = threshold
	cfg.RedisHost = os.Getenv("REDIS_HOST")
	cfg.ForwardedAllowIPs = os.Getenv("FORWARDED_ALLOW_IPS")
	cfg.AllowedUpstreamHosts = loadAllowedUpstreamHosts()
	cfg.CachePolicy, err = loadCachePolicy()
	if err != nil {
		return Config{}, err
	}
	cfg.Port = os.Getenv("PORT")
	if cfg.Port == "" {
		cfg.Port = defaultPort
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

	policy.RevalidateEndpointsPerWorker, err = parseEnvPositiveInt(
		"REVALIDATE_ENDPOINTS_PER_WORKER",
		policy.RevalidateEndpointsPerWorker,
	)
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

	policy.WriteBehindQueueSize, err = parseEnvPositiveInt("WRITE_BEHIND_QUEUE_SIZE", policy.WriteBehindQueueSize)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.WriteBehindFlushInterval, err = parseEnvDuration(
		"WRITE_BEHIND_FLUSH_INTERVAL",
		policy.WriteBehindFlushInterval,
	)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.WriteBehindRetryMaxInterval, err = parseEnvDuration(
		"WRITE_BEHIND_RETRY_MAX_INTERVAL",
		policy.WriteBehindRetryMaxInterval,
	)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.WriteBehindRetryMaxAge, err = parseEnvDuration(
		"WRITE_BEHIND_RETRY_MAX_AGE",
		policy.WriteBehindRetryMaxAge,
	)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.L1MaxGB, err = parseEnvPositiveFloat("CACHE_L1_MAX_GB", policy.L1MaxGB)
	if err != nil {
		return cache.Policy{}, err
	}

	policy.ShutdownDrainTimeout, err = parseEnvDuration("SHUTDOWN_DRAIN_TIMEOUT", policy.ShutdownDrainTimeout)
	if err != nil {
		return cache.Policy{}, err
	}

	return policy, nil
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
