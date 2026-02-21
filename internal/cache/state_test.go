package cache

import (
	"testing"
	"time"
)

func TestIsRevalidationEligible(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 18, 0, 0, 0, time.UTC)
	lookback := 7 * 24 * time.Hour
	threshold := now.Add(-lookback)

	tests := []struct {
		name      string
		lastHitAt time.Time
		want      bool
	}{
		{
			name:      "eligible after threshold",
			lastHitAt: threshold.Add(time.Nanosecond),
			want:      true,
		},
		{
			name:      "eligible on threshold",
			lastHitAt: threshold,
			want:      true,
		},
		{
			name:      "not eligible before threshold",
			lastHitAt: threshold.Add(-time.Nanosecond),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := IsRevalidationEligible(tt.lastHitAt, now, lookback)
			if got != tt.want {
				t.Fatalf("expected %t, got %t", tt.want, got)
			}
		})
	}
}

func TestIsRevalidationDue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 21, 18, 0, 0, 0, time.UTC)
	interval := time.Minute

	tests := []struct {
		name          string
		lastCheckedAt time.Time
		want          bool
	}{
		{
			name:          "due before interval boundary",
			lastCheckedAt: now.Add(-interval).Add(-time.Nanosecond),
			want:          true,
		},
		{
			name:          "due at interval boundary",
			lastCheckedAt: now.Add(-interval),
			want:          true,
		},
		{
			name:          "not due before interval has elapsed",
			lastCheckedAt: now.Add(-interval).Add(time.Nanosecond),
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := IsRevalidationDue(tt.lastCheckedAt, now, interval)
			if got != tt.want {
				t.Fatalf("expected %t, got %t", tt.want, got)
			}
		})
	}
}
