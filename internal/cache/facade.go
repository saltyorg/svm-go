package cache

import (
	"context"
	"errors"
)

var (
	// ErrCacheFacadeKeyRequired indicates facade operations were called without a key.
	ErrCacheFacadeKeyRequired = errors.New("cache key is required")
)

// L1Store defines the in-memory cache surface used by the facade.
type L1Store interface {
	Get(key string) (Record, bool)
	Set(key string, record Record)
}

// L1ReadonlyStore defines an optional read-only retrieval path that may return aliased payload bytes.
type L1ReadonlyStore interface {
	GetReadonly(key string) (Record, bool)
}

// L2Store defines the persistence/hydration surface used by the facade.
type L2Store interface {
	Get(ctx context.Context, key string) (*Record, bool, error)
}

// WriteBehindEnqueuer defines non-blocking persistence handoff behavior.
type WriteBehindEnqueuer interface {
	Enqueue(key string, record Record) bool
}

// Facade composes L1 and L2 cache tiers with async write-behind handoff.
type Facade struct {
	l1          L1Store
	l2          L2Store
	writeBehind WriteBehindEnqueuer
}

// NewFacade creates a cache facade with the provided tier and write-behind adapters.
func NewFacade(l1 L1Store, l2 L2Store, writeBehind WriteBehindEnqueuer) *Facade {
	return &Facade{
		l1:          l1,
		l2:          l2,
		writeBehind: writeBehind,
	}
}

// Get reads from L1 first, then attempts L2 hydration and backfills L1 on L2 hit.
func (f *Facade) Get(ctx context.Context, key string) (Record, bool, error) {
	if key == "" {
		return Record{}, false, ErrCacheFacadeKeyRequired
	}

	if f != nil && f.l1 != nil {
		record, hit := f.l1.Get(key)
		if hit {
			return record, true, nil
		}
	}

	if f == nil || f.l2 == nil {
		return Record{}, false, nil
	}

	record, hit, err := f.l2.Get(ctx, key)
	if err != nil {
		return Record{}, false, err
	}
	if !hit || record == nil {
		return Record{}, false, nil
	}

	if f.l1 != nil {
		f.l1.Set(key, *record)
	}

	return *record, true, nil
}

// GetReadonly reads from L1 first and may return payload bytes aliased to cache storage on hit.
func (f *Facade) GetReadonly(ctx context.Context, key string) (Record, bool, error) {
	if key == "" {
		return Record{}, false, ErrCacheFacadeKeyRequired
	}

	if f != nil && f.l1 != nil {
		if l1Readonly, ok := f.l1.(L1ReadonlyStore); ok {
			record, hit := l1Readonly.GetReadonly(key)
			if hit {
				return record, true, nil
			}
		} else {
			record, hit := f.l1.Get(key)
			if hit {
				return record, true, nil
			}
		}
	}

	if f == nil || f.l2 == nil {
		return Record{}, false, nil
	}

	record, hit, err := f.l2.Get(ctx, key)
	if err != nil {
		return Record{}, false, err
	}
	if !hit || record == nil {
		return Record{}, false, nil
	}

	if f.l1 != nil {
		f.l1.Set(key, *record)
	}

	return *record, true, nil
}

// Set writes to L1 first and then attempts async write-behind enqueue.
func (f *Facade) Set(key string, record Record) (bool, error) {
	if key == "" {
		return false, ErrCacheFacadeKeyRequired
	}
	if f == nil {
		return false, nil
	}

	if f.l1 != nil {
		f.l1.Set(key, record)
	}
	if f.writeBehind == nil {
		return false, nil
	}

	return f.writeBehind.Enqueue(key, record), nil
}

func cloneFacadeRecord(record Record) Record {
	cloned := record
	cloned.Payload = append([]byte(nil), record.Payload...)
	return cloned
}
