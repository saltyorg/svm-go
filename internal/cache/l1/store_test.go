package l1

import (
	"context"
	"testing"

	"svm/internal/cache"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewStore(NewCacheWithMaxBytes(1 << 20))
	record := cache.Record{Payload: []byte(`{"tag_name":"v1"}`)}
	stored, err := store.Set("key", record)
	if err != nil || !stored {
		t.Fatalf("expected successful set, stored=%v err=%v", stored, err)
	}

	got, hit, err := store.Get(context.Background(), "key")
	if err != nil || !hit {
		t.Fatalf("expected cache hit, hit=%v err=%v", hit, err)
	}
	if string(got.Payload) != string(record.Payload) {
		t.Fatalf("expected payload %s, got %s", record.Payload, got.Payload)
	}
}
