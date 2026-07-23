package version

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestActivityTrackerRecordsOrdersLimitsAndPrunes(t *testing.T) {
	t.Parallel()

	tracker := NewActivityTracker()
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tracker.RecordActivity("old", base)
	tracker.RecordActivity("newest", base.Add(2*time.Minute))
	tracker.RecordActivity("newer", base.Add(time.Minute))

	keys, err := tracker.ActiveKeysSince(context.Background(), base, 2)
	if err != nil {
		t.Fatalf("list active keys: %v", err)
	}
	if want := []string{"old", "newer"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("expected keys %v, got %v", want, keys)
	}

	if err := tracker.PruneActiveKeysBefore(context.Background(), base.Add(time.Minute)); err != nil {
		t.Fatalf("prune active keys: %v", err)
	}
	keys, err = tracker.ActiveKeysSince(context.Background(), time.Time{}, 0)
	if err != nil {
		t.Fatalf("list keys after prune: %v", err)
	}
	if want := []string{"newer", "newest"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("expected keys %v after prune, got %v", want, keys)
	}
}

func TestActivityTrackerKeepsNewestTimestamp(t *testing.T) {
	t.Parallel()

	tracker := NewActivityTracker()
	newer := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tracker.RecordActivity("key", newer)
	tracker.RecordActivity("key", newer.Add(-time.Hour))

	keys, err := tracker.ActiveKeysSince(context.Background(), newer, 0)
	if err != nil {
		t.Fatalf("list active keys: %v", err)
	}
	if want := []string{"key"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("expected keys %v, got %v", want, keys)
	}
}
