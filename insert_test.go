package oban

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// insertFakeStore is a configurable [Store] used to exercise the [Insert] paths
// deterministically. It optionally implements [UniqueStore] and [TaggableStore].
type insertFakeStore struct {
	nextID    int64
	enqueued  []*Job
	enqErr    error
	conflict  *Job
	findErr   error
	lastBy    UniqueBy
	findCalls int
	tags      map[int64][]string
	meta      map[int64]map[string]any
}

func newInsertFakeStore() *insertFakeStore {
	return &insertFakeStore{
		tags: map[int64][]string{},
		meta: map[int64]map[string]any{},
	}
}

func (s *insertFakeStore) Enqueue(_ context.Context, job *Job) (*Job, bool, error) {
	if s.enqErr != nil {
		return nil, false, s.enqErr
	}
	s.nextID++
	stored := job.Clone()
	stored.ID = s.nextID
	s.enqueued = append(s.enqueued, stored)
	return stored.Clone(), true, nil
}

func (s *insertFakeStore) FetchAvailable(context.Context, string, int, time.Time) ([]*Job, error) {
	return nil, nil
}
func (s *insertFakeStore) Complete(context.Context, int64, time.Time) error { return nil }
func (s *insertFakeStore) Retry(context.Context, int64, time.Time, error, time.Time) error {
	return nil
}
func (s *insertFakeStore) Discard(context.Context, int64, error, time.Time) error { return nil }
func (s *insertFakeStore) Get(context.Context, int64) (*Job, error)               { return nil, ErrJobNotFound }

func (s *insertFakeStore) FindConflict(_ context.Context, _ *Job, by UniqueBy, _ time.Time) (*Job, error) {
	s.findCalls++
	s.lastBy = by
	if s.findErr != nil {
		return nil, s.findErr
	}
	if s.conflict == nil {
		return nil, nil
	}
	return s.conflict, nil
}

func (s *insertFakeStore) SetTagsMeta(_ context.Context, id int64, tags []string, meta map[string]any) error {
	s.tags[id] = tags
	s.meta[id] = meta
	return nil
}

func (s *insertFakeStore) TagsMeta(_ context.Context, id int64) ([]string, map[string]any, error) {
	return s.tags[id], s.meta[id], nil
}

func TestValidatePriority(t *testing.T) {
	tests := []struct {
		name    string
		p       int
		wantErr bool
	}{
		{"negative", -1, true},
		{"zero", 0, false},
		{"mid", 5, false},
		{"max", 9, false},
		{"tooHigh", 10, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePriority(tc.p)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePriority(%d) err=%v, wantErr=%v", tc.p, err, tc.wantErr)
			}
		})
	}
}

func TestInsertRejectsBadPriority(t *testing.T) {
	store := newInsertFakeStore()
	job, err := NewJob("W", map[string]any{"a": 1}, WithPriority(10))
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if _, _, err := Insert(context.Background(), store, job, InsertOpts{}); err == nil {
		t.Fatal("expected error for priority 10")
	}
	if len(store.enqueued) != 0 {
		t.Fatalf("job should not have been enqueued, got %d", len(store.enqueued))
	}
}

func TestInsertNilJob(t *testing.T) {
	if _, _, err := Insert(context.Background(), newInsertFakeStore(), nil, InsertOpts{}); err == nil {
		t.Fatal("expected error for nil job")
	}
}

func TestInsertDelegatesAndTags(t *testing.T) {
	store := newInsertFakeStore()
	job, _ := NewJob("W", map[string]any{"a": 1})
	res, inserted, err := Insert(context.Background(), store, job, InsertOpts{
		Tags: []string{"x", "y"},
		Meta: map[string]any{"k": "v"},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !inserted {
		t.Fatal("want inserted=true")
	}
	if len(store.enqueued) != 1 {
		t.Fatalf("want 1 enqueued, got %d", len(store.enqueued))
	}
	if got := store.tags[res.ID]; !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Fatalf("tags not persisted, got %v", got)
	}
	if got := store.meta[res.ID]; !reflect.DeepEqual(got, map[string]any{"k": "v"}) {
		t.Fatalf("meta not persisted, got %v", got)
	}
}

func TestInsertUniqueConflictNoReplace(t *testing.T) {
	store := newInsertFakeStore()
	store.conflict = &Job{ID: 42, Worker: "W", Queue: "default"}
	job, _ := NewJob("W", map[string]any{"a": 1})
	res, inserted, err := Insert(context.Background(), store, job, InsertOpts{
		Unique: &UniqueBy{Period: time.Hour},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if inserted {
		t.Fatal("want inserted=false on conflict")
	}
	if res.ID != 42 {
		t.Fatalf("want conflict job, got ID %d", res.ID)
	}
	if len(store.enqueued) != 0 {
		t.Fatal("conflict should not enqueue")
	}
	// Defaults must be resolved before reaching the store.
	if len(store.lastBy.States) == 0 || len(store.lastBy.Fields) == 0 {
		t.Fatalf("defaults not applied: %+v", store.lastBy)
	}
}

func TestInsertUniqueConflictReplaceScalars(t *testing.T) {
	store := newInsertFakeStore()
	store.conflict = &Job{
		ID:          7,
		ScheduledAt: baseTime,
		Priority:    1,
		MaxAttempts: 5,
		Args:        json.RawMessage(`{"old":true}`),
	}
	newSched := baseTime.Add(time.Hour)
	job, _ := NewJob("W", map[string]any{"new": true}, WithPriority(3), WithMaxAttempts(9))
	job.ScheduledAt = newSched
	res, inserted, err := Insert(context.Background(), store, job, InsertOpts{
		Unique:  &UniqueBy{Period: time.Hour},
		Replace: []ReplaceField{ReplaceScheduledAt, ReplacePriority, ReplaceMaxAttempts, ReplaceArgs},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if inserted {
		t.Fatal("want inserted=false")
	}
	if !res.ScheduledAt.Equal(newSched) {
		t.Fatalf("ScheduledAt not replaced: %v", res.ScheduledAt)
	}
	if res.Priority != 3 || res.MaxAttempts != 9 {
		t.Fatalf("scalar replace failed: prio=%d max=%d", res.Priority, res.MaxAttempts)
	}
	if string(res.Args) != `{"new":true}` {
		t.Fatalf("Args not replaced: %s", res.Args)
	}
}

func TestInsertUniqueConflictReplaceTagsMeta(t *testing.T) {
	store := newInsertFakeStore()
	store.conflict = &Job{ID: 11}
	store.tags[11] = []string{"old"}
	store.meta[11] = map[string]any{"o": 1}
	job, _ := NewJob("W", map[string]any{"a": 1})
	_, inserted, err := Insert(context.Background(), store, job, InsertOpts{
		Unique:  &UniqueBy{Period: time.Hour},
		Replace: []ReplaceField{ReplaceTags, ReplaceMeta},
		Tags:    []string{"new"},
		Meta:    map[string]any{"n": 2},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if inserted {
		t.Fatal("want inserted=false")
	}
	if got := store.tags[11]; !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("tags not replaced: %v", got)
	}
	if got := store.meta[11]; !reflect.DeepEqual(got, map[string]any{"n": 2}) {
		t.Fatalf("meta not replaced: %v", got)
	}
}

func TestInsertUniqueNoConflictEnqueues(t *testing.T) {
	store := newInsertFakeStore()
	job, _ := NewJob("W", map[string]any{"a": 1})
	_, inserted, err := Insert(context.Background(), store, job, InsertOpts{
		Unique: &UniqueBy{Period: time.Hour, Fields: []UniqueField{UniqueWorker}},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !inserted {
		t.Fatal("want inserted=true")
	}
	if store.findCalls != 1 {
		t.Fatalf("FindConflict called %d times", store.findCalls)
	}
	if len(store.enqueued) != 1 {
		t.Fatal("want enqueue after no conflict")
	}
}

func TestInsertFindConflictError(t *testing.T) {
	store := newInsertFakeStore()
	store.findErr = errors.New("boom")
	job, _ := NewJob("W", map[string]any{"a": 1})
	if _, _, err := Insert(context.Background(), store, job, InsertOpts{Unique: &UniqueBy{Period: time.Hour}}); err == nil {
		t.Fatal("expected propagated FindConflict error")
	}
}

func TestInsertFallbackMapsUniqueKey(t *testing.T) {
	// InMemoryStore does not implement UniqueStore, so Insert must fold the
	// unique request onto job.UniqueKey/UniquePeriod for built-in dedupe.
	store := NewInMemoryStore()
	mkJob := func() *Job {
		j, _ := NewJob("W", map[string]any{"a": 1})
		j.InsertedAt = baseTime
		return j
	}
	opts := InsertOpts{Unique: &UniqueBy{Period: time.Hour}}

	res1, ins1, err := Insert(context.Background(), store, mkJob(), opts)
	if err != nil || !ins1 {
		t.Fatalf("first insert: inserted=%v err=%v", ins1, err)
	}
	res2, ins2, err := Insert(context.Background(), store, mkJob(), opts)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if ins2 {
		t.Fatal("second insert should have de-duplicated")
	}
	if res1.ID != res2.ID {
		t.Fatalf("dedupe returned different job: %d vs %d", res1.ID, res2.ID)
	}
}

func TestTagsUnsupportedStore(t *testing.T) {
	tags, err := Tags(context.Background(), NewInMemoryStore(), 1)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("want empty tags, got %v", tags)
	}
}

func TestTagsSupportedStore(t *testing.T) {
	store := newInsertFakeStore()
	store.tags[5] = []string{"a", "b"}
	tags, err := Tags(context.Background(), store, 5)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if !reflect.DeepEqual(tags, []string{"a", "b"}) {
		t.Fatalf("want [a b], got %v", tags)
	}
}

func TestInsertUniqueKeyDeterministic(t *testing.T) {
	by := UniqueBy{Period: time.Hour}.insertWithDefaults()
	j1, _ := NewJob("W", json.RawMessage(`{"a":1,"b":2}`))
	j2, _ := NewJob("W", json.RawMessage(`{"b":2,"a":1}`))
	k1 := insertUniqueKey(j1, by)
	k2 := insertUniqueKey(j2, by)
	if k1 != k2 {
		t.Fatalf("keys differ for equal args: %q vs %q", k1, k2)
	}
	j3, _ := NewJob("W", json.RawMessage(`{"a":9}`))
	if insertUniqueKey(j3, by) == k1 {
		t.Fatal("distinct args produced identical key")
	}
}

func TestInsertWithDefaults(t *testing.T) {
	by := UniqueBy{Period: time.Minute}.insertWithDefaults()
	wantStates := []State{StateAvailable, StateScheduled, StateExecuting, StateRetryable}
	if !reflect.DeepEqual(by.States, wantStates) {
		t.Fatalf("default states = %v", by.States)
	}
	wantFields := []UniqueField{UniqueWorker, UniqueQueue, UniqueArgs}
	if !reflect.DeepEqual(by.Fields, wantFields) {
		t.Fatalf("default fields = %v", by.Fields)
	}
}
