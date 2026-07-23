package cache

import "time"

// Record is the canonical in-memory cache payload and metadata envelope.
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
