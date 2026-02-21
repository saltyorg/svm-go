package cache

import "time"

// Record is the canonical cache payload and metadata envelope shared by L1/L2.
type Record struct {
	Payload       []byte    `json:"payload"`
	ETag          string    `json:"etag"`
	URL           string    `json:"url,omitempty"`
	FetchedAt     time.Time `json:"fetched_at"`
	LastHitAt     time.Time `json:"last_hit_at"`
	LastCheckedAt time.Time `json:"last_checked_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	SourceStatus  int       `json:"source_status"`
}
