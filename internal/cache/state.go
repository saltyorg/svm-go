package cache

import "time"

// IsRevalidationEligible reports whether a key was hit within the lookback window.
func IsRevalidationEligible(lastHitAt, now time.Time, lookback time.Duration) bool {
	return !lastHitAt.Before(now.Add(-lookback))
}

// IsRevalidationDue reports whether a key has reached the next check interval.
func IsRevalidationDue(lastCheckedAt, now time.Time, interval time.Duration) bool {
	return !lastCheckedAt.Add(interval).After(now)
}
