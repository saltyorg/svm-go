package cache

import "time"

const (
	defaultHardTTL                  = 24 * time.Hour
	defaultNegativeTTL              = 5 * time.Minute
	defaultRevalidateInterval       = time.Minute
	defaultRevalidateLookback       = 7 * 24 * time.Hour
	defaultRevalidateWorkers        = 2
	defaultRevalidatePerWorkerRPS   = 1
	defaultRefreshQueueSize         = 256
	defaultL1MaxGB                  = 1.0
	defaultMaxUpstreamResponseBytes = 10 * 1024 * 1024
	defaultShutdownTimeout          = 2 * time.Second
)

// Policy holds cache behavior and worker tuning knobs.
type Policy struct {
	HardTTL                  time.Duration
	NegativeTTL              time.Duration
	RevalidateInterval       time.Duration
	RevalidateLookback       time.Duration
	RevalidateWorkers        int
	RevalidatePerWorkerRPS   int
	RefreshQueueSize         int
	L1MaxGB                  float64
	MaxUpstreamResponseBytes int64
	ShutdownTimeout          time.Duration
}

// DefaultPolicy returns conservative defaults for low-throughput deployments.
func DefaultPolicy() Policy {
	return Policy{
		HardTTL:                  defaultHardTTL,
		NegativeTTL:              defaultNegativeTTL,
		RevalidateInterval:       defaultRevalidateInterval,
		RevalidateLookback:       defaultRevalidateLookback,
		RevalidateWorkers:        defaultRevalidateWorkers,
		RevalidatePerWorkerRPS:   defaultRevalidatePerWorkerRPS,
		RefreshQueueSize:         defaultRefreshQueueSize,
		L1MaxGB:                  defaultL1MaxGB,
		MaxUpstreamResponseBytes: defaultMaxUpstreamResponseBytes,
		ShutdownTimeout:          defaultShutdownTimeout,
	}
}
