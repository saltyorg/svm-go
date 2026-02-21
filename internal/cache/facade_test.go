package cache

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFacadeGetPrefersL1(t *testing.T) {
	t.Parallel()

	record := Record{Payload: []byte(`{"tag_name":"v1.2.3"}`), SourceStatus: 200}
	l1 := &fakeL1Store{
		records: map[string]Record{
			"key-1": record,
		},
	}
	l2 := &fakeL2Store{
		record: &Record{Payload: []byte(`{"tag_name":"v9.9.9"}`), SourceStatus: 200},
		hit:    true,
	}
	facade := NewFacade(l1, l2, nil)

	got, hit, err := facade.Get(context.Background(), "key-1")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !hit {
		t.Fatal("expected L1 hit=true")
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("expected L1 record %+v, got %+v", record, got)
	}
	if l2.getCalls != 0 {
		t.Fatalf("expected L2 get calls 0 on L1 hit, got %d", l2.getCalls)
	}
}

func TestFacadeGetBackfillsL1OnL2Hit(t *testing.T) {
	t.Parallel()

	l1 := &fakeL1Store{records: map[string]Record{}}
	l2Record := &Record{Payload: []byte(`{"tag_name":"v2.0.0"}`), ETag: `"etag-1"`, SourceStatus: 200}
	l2 := &fakeL2Store{
		record: l2Record,
		hit:    true,
	}
	facade := NewFacade(l1, l2, nil)

	got, hit, err := facade.Get(context.Background(), "key-2")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !hit {
		t.Fatal("expected L2 hit=true")
	}
	if !reflect.DeepEqual(got, *l2Record) {
		t.Fatalf("expected hydrated record %+v, got %+v", *l2Record, got)
	}

	if l1.setCalls != 1 {
		t.Fatalf("expected one L1 backfill set, got %d", l1.setCalls)
	}
	l1Got, ok := l1.records["key-2"]
	if !ok {
		t.Fatal("expected key-2 to be backfilled into L1")
	}
	if !reflect.DeepEqual(l1Got, *l2Record) {
		t.Fatalf("expected L1 backfill %+v, got %+v", *l2Record, l1Got)
	}
}

func TestFacadeSetWritesL1BeforeWriteBehindEnqueue(t *testing.T) {
	t.Parallel()

	callOrder := make([]string, 0, 2)
	l1 := &fakeL1Store{
		records:    map[string]Record{},
		callOrder:  &callOrder,
		setCallTag: "l1-set",
	}
	writer := &fakeWriteBehind{
		callOrder:   &callOrder,
		enqueueTag:  "write-behind-enqueue",
		enqueueResp: true,
	}
	facade := NewFacade(l1, nil, writer)

	record := Record{Payload: []byte(`{"tag_name":"v3.1.4"}`), SourceStatus: 200}
	enqueued, err := facade.Set("key-3", record)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !enqueued {
		t.Fatal("expected write-behind enqueue true")
	}

	wantOrder := []string{"l1-set", "write-behind-enqueue"}
	if !reflect.DeepEqual(callOrder, wantOrder) {
		t.Fatalf("expected call order %v, got %v", wantOrder, callOrder)
	}

	l1Got, ok := l1.records["key-3"]
	if !ok {
		t.Fatal("expected key-3 persisted in L1")
	}
	if !reflect.DeepEqual(l1Got, record) {
		t.Fatalf("expected L1 record %+v, got %+v", record, l1Got)
	}
	if writer.enqueueCalls != 1 {
		t.Fatalf("expected one write-behind enqueue, got %d", writer.enqueueCalls)
	}
	if writer.lastKey != "key-3" {
		t.Fatalf("expected write-behind key %q, got %q", "key-3", writer.lastKey)
	}
	if !reflect.DeepEqual(writer.lastRecord, record) {
		t.Fatalf("expected write-behind record %+v, got %+v", record, writer.lastRecord)
	}
}

func TestFacadeSetPersistsToL1WhenWriteBehindDrops(t *testing.T) {
	t.Parallel()

	l1 := &fakeL1Store{records: map[string]Record{}}
	writer := &fakeWriteBehind{enqueueResp: false}
	facade := NewFacade(l1, nil, writer)

	record := Record{Payload: []byte(`{"tag_name":"v4.0.0"}`), SourceStatus: 200}
	enqueued, err := facade.Set("key-4", record)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if enqueued {
		t.Fatal("expected write-behind enqueue false when queue drops event")
	}
	if writer.enqueueCalls != 1 {
		t.Fatalf("expected one write-behind enqueue attempt, got %d", writer.enqueueCalls)
	}

	l1Got, ok := l1.records["key-4"]
	if !ok {
		t.Fatal("expected key-4 persisted in L1 despite write-behind drop")
	}
	if !reflect.DeepEqual(l1Got, record) {
		t.Fatalf("expected L1 record %+v, got %+v", record, l1Got)
	}
}

func TestFacadeGetReturnsL2Error(t *testing.T) {
	t.Parallel()

	l2Err := errors.New("redis unavailable")
	facade := NewFacade(&fakeL1Store{records: map[string]Record{}}, &fakeL2Store{err: l2Err}, nil)

	_, hit, err := facade.Get(context.Background(), "key-4")
	if err == nil {
		t.Fatal("expected error on L2 failure, got nil")
	}
	if !errors.Is(err, l2Err) {
		t.Fatalf("expected error %v, got %v", l2Err, err)
	}
	if hit {
		t.Fatal("expected hit=false on L2 failure")
	}
}

type fakeL1Store struct {
	records    map[string]Record
	getCalls   int
	setCalls   int
	callOrder  *[]string
	setCallTag string
}

func (f *fakeL1Store) Get(key string) (Record, bool) {
	f.getCalls++
	record, ok := f.records[key]
	if !ok {
		return Record{}, false
	}

	return cloneFacadeRecord(record), true
}

func (f *fakeL1Store) Set(key string, record Record) {
	f.setCalls++
	if f.callOrder != nil && f.setCallTag != "" {
		*f.callOrder = append(*f.callOrder, f.setCallTag)
	}
	f.records[key] = cloneFacadeRecord(record)
}

type fakeL2Store struct {
	record   *Record
	hit      bool
	err      error
	getCalls int
}

func (f *fakeL2Store) Get(_ context.Context, _ string) (*Record, bool, error) {
	f.getCalls++
	if f.err != nil {
		return nil, false, f.err
	}
	if !f.hit || f.record == nil {
		return nil, false, nil
	}

	record := cloneFacadeRecord(*f.record)
	return &record, true, nil
}

type fakeWriteBehind struct {
	enqueueCalls int
	lastKey      string
	lastRecord   Record
	enqueueResp  bool
	callOrder    *[]string
	enqueueTag   string
}

func (f *fakeWriteBehind) Enqueue(key string, record Record) bool {
	f.enqueueCalls++
	f.lastKey = key
	f.lastRecord = cloneFacadeRecord(record)
	if f.callOrder != nil && f.enqueueTag != "" {
		*f.callOrder = append(*f.callOrder, f.enqueueTag)
	}
	return f.enqueueResp
}
