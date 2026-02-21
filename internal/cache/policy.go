package cache

import "time"

const (
	defaultHardTTL                   = 24 * time.Hour
	defaultNegativeTTL               = 5 * time.Minute
	defaultRevalidateInterval        = time.Minute
	defaultRevalidateLookback        = 7 * 24 * time.Hour
	defaultRevalidateEndpointsWorker = 30
	defaultRevalidatePerWorkerRPS    = 1
	defaultWriteBehindQueueSize      = 256
	defaultWriteBehindFlushInterval  = time.Second
	defaultRetryMaxInterval          = 30 * time.Second
	defaultRetryMaxAge               = 5 * time.Minute
	defaultL1MaxGB                   = 1.0
	defaultShutdownDrainTimeout      = 2 * time.Second
)

// Policy holds cache behavior and worker tuning knobs.
type Policy struct {
	HardTTL                      time.Duration
	NegativeTTL                  time.Duration
	RevalidateInterval           time.Duration
	RevalidateLookback           time.Duration
	RevalidateEndpointsPerWorker int
	RevalidatePerWorkerRPS       int
	WriteBehindQueueSize         int
	WriteBehindFlushInterval     time.Duration
	WriteBehindRetryMaxInterval  time.Duration
	WriteBehindRetryMaxAge       time.Duration
	L1MaxGB                      float64
	ShutdownDrainTimeout         time.Duration
}

// DefaultPolicy returns conservative defaults for low-throughput deployments.
func DefaultPolicy() Policy {
	return Policy{
		HardTTL:                      defaultHardTTL,
		NegativeTTL:                  defaultNegativeTTL,
		RevalidateInterval:           defaultRevalidateInterval,
		RevalidateLookback:           defaultRevalidateLookback,
		RevalidateEndpointsPerWorker: defaultRevalidateEndpointsWorker,
		RevalidatePerWorkerRPS:       defaultRevalidatePerWorkerRPS,
		WriteBehindQueueSize:         defaultWriteBehindQueueSize,
		WriteBehindFlushInterval:     defaultWriteBehindFlushInterval,
		WriteBehindRetryMaxInterval:  defaultRetryMaxInterval,
		WriteBehindRetryMaxAge:       defaultRetryMaxAge,
		L1MaxGB:                      defaultL1MaxGB,
		ShutdownDrainTimeout:         defaultShutdownDrainTimeout,
	}
}
