package oban

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// drainCapStore is an InMemoryStore extended with the optional cancel and snooze
// capabilities so the sentinel handling in Drain can be exercised. It records
// which job IDs were cancelled or snoozed.
type drainCapStore struct {
	*InMemoryStore
	cancelled []int64
	snoozed   []int64
}

func newDrainCapStore() *drainCapStore {
	return &drainCapStore{InMemoryStore: NewInMemoryStore()}
}

func (s *drainCapStore) Cancel(ctx context.Context, id int64, now time.Time) error {
	s.cancelled = append(s.cancelled, id)
	return s.Discard(ctx, id, fmt.Errorf("oban: cancelled"), now)
}

func (s *drainCapStore) Snooze(ctx context.Context, id int64, scheduledAt time.Time, now time.Time) error {
	s.snoozed = append(s.snoozed, id)
	return s.Retry(ctx, id, scheduledAt, nil, now)
}

// drainNilWorker completes immediately.
func drainNilWorker(context.Context, *Job) error { return nil }

// drainErrWorker always fails.
func drainErrWorker(context.Context, *Job) error { return fmt.Errorf("boom") }

// drainFailThenSucceed fails on the first attempt and succeeds afterwards.
func drainFailThenSucceed(_ context.Context, job *Job) error {
	if job.Attempt < 2 {
		return fmt.Errorf("boom")
	}
	return nil
}

// drainCancelWorker returns the cancel sentinel.
func drainCancelWorker(context.Context, *Job) error { return CancelJob("") }

// drainSnoozeWorker returns the snooze sentinel.
func drainSnoozeWorker(context.Context, *Job) error { return Snooze(time.Second) }

// drainEnqueue inserts a job for worker into store using a fixed insertion time.
func drainEnqueue(t *testing.T, store Store, worker string, opts ...JobOption) *Job {
	t.Helper()
	job, err := NewJob(worker, nil, opts...)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	job.InsertedAt = baseTime
	stored, _, err := store.Enqueue(context.Background(), job)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return stored
}

func TestDrainResultTotal(t *testing.T) {
	r := DrainResult{Completed: 1, Retried: 2, Discarded: 3, Cancelled: 4, Snoozed: 5, Failed: 6}
	if got, want := r.Total(), 21; got != want {
		t.Fatalf("Total() = %d, want %d", got, want)
	}
	if (DrainResult{}).Total() != 0 {
		t.Fatalf("zero DrainResult Total() must be 0")
	}
}

func TestDrain(t *testing.T) {
	tests := []struct {
		name       string
		worker     func(context.Context, *Job) error
		register   bool // register the worker under "w"
		enqueueFor string
		jobOpts    []JobOption
		opts       DrainOptions
		want       DrainResult
	}{
		{
			name:       "completed",
			worker:     drainNilWorker,
			register:   true,
			enqueueFor: "w",
			want:       DrainResult{Completed: 1},
		},
		{
			name:       "retried when attempts remain",
			worker:     drainErrWorker,
			register:   true,
			enqueueFor: "w",
			jobOpts:    []JobOption{WithMaxAttempts(3)},
			want:       DrainResult{Retried: 1},
		},
		{
			name:       "discarded when attempts exhausted",
			worker:     drainErrWorker,
			register:   true,
			enqueueFor: "w",
			jobOpts:    []JobOption{WithMaxAttempts(1)},
			want:       DrainResult{Discarded: 1},
		},
		{
			name:       "failed when worker unregistered",
			register:   false,
			enqueueFor: "missing",
			want:       DrainResult{Failed: 1},
		},
		{
			name:       "single pass does not follow retries",
			worker:     drainFailThenSucceed,
			register:   true,
			enqueueFor: "w",
			opts:       DrainOptions{},
			want:       DrainResult{Retried: 1},
		},
		{
			name:       "recursion runs retried-then-ready job to completion",
			worker:     drainFailThenSucceed,
			register:   true,
			enqueueFor: "w",
			opts:       DrainOptions{WithRecursion: true, WithScheduled: true},
			want:       DrainResult{Retried: 1, Completed: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewInMemoryStore()
			reg := NewRegistry()
			if tc.register {
				reg.RegisterFunc("w", tc.worker)
			}
			drainEnqueue(t, store, tc.enqueueFor, tc.jobOpts...)

			opts := tc.opts
			opts.Queue = DefaultQueue
			got, err := Drain(context.Background(), store, reg, newFakeClock(baseTime), opts)
			if err != nil {
				t.Fatalf("Drain: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DrainResult = %+v, want %+v", got, tc.want)
			}
			if got.Total() != tc.want.Total() {
				t.Fatalf("Total() = %d, want %d", got.Total(), tc.want.Total())
			}
		})
	}
}

func TestDrainSentinels(t *testing.T) {
	tests := []struct {
		name        string
		worker      func(context.Context, *Job) error
		capable     bool // use the cancel/snooze-capable store
		want        DrainResult
		wantCancels int
		wantSnoozes int
	}{
		{
			name:        "cancel sentinel with capable store",
			worker:      drainCancelWorker,
			capable:     true,
			want:        DrainResult{Cancelled: 1},
			wantCancels: 1,
		},
		{
			name:        "snooze sentinel with capable store",
			worker:      drainSnoozeWorker,
			capable:     true,
			want:        DrainResult{Snoozed: 1},
			wantSnoozes: 1,
		},
		{
			name:    "cancel sentinel falls back to retry without capability",
			worker:  drainCancelWorker,
			capable: false,
			want:    DrainResult{Retried: 1},
		},
		{
			name:    "snooze sentinel falls back to retry without capability",
			worker:  drainSnoozeWorker,
			capable: false,
			want:    DrainResult{Retried: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			reg.RegisterFunc("w", tc.worker)

			var store Store
			var capStore *drainCapStore
			if tc.capable {
				capStore = newDrainCapStore()
				store = capStore
			} else {
				// caplessStore lacks the optional capabilities; InMemoryStore
				// itself now implements them, so the incapable path uses this.
				store = newCaplessStore()
			}
			drainEnqueue(t, store, "w")

			got, err := Drain(context.Background(), store, reg, newFakeClock(baseTime), DrainOptions{Queue: DefaultQueue})
			if err != nil {
				t.Fatalf("Drain: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DrainResult = %+v, want %+v", got, tc.want)
			}
			if capStore != nil {
				if len(capStore.cancelled) != tc.wantCancels {
					t.Fatalf("cancelled = %d, want %d", len(capStore.cancelled), tc.wantCancels)
				}
				if len(capStore.snoozed) != tc.wantSnoozes {
					t.Fatalf("snoozed = %d, want %d", len(capStore.snoozed), tc.wantSnoozes)
				}
			}
		})
	}
}

func TestDrainWithScheduled(t *testing.T) {
	tests := []struct {
		name          string
		withScheduled bool
		want          DrainResult
	}{
		{name: "future job skipped by default", withScheduled: false, want: DrainResult{}},
		{name: "future job run when WithScheduled", withScheduled: true, want: DrainResult{Completed: 1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewInMemoryStore()
			reg := NewRegistry()
			reg.RegisterFunc("w", drainNilWorker)
			drainEnqueue(t, store, "w", WithScheduledAt(baseTime.Add(time.Hour)))

			got, err := Drain(context.Background(), store, reg, newFakeClock(baseTime), DrainOptions{
				Queue:         DefaultQueue,
				WithScheduled: tc.withScheduled,
			})
			if err != nil {
				t.Fatalf("Drain: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DrainResult = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDrainMiddlewareOrder(t *testing.T) {
	var order []string
	mw := func(tag string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, job *Job) error {
				order = append(order, "before:"+tag)
				err := next(ctx, job)
				order = append(order, "after:"+tag)
				return err
			}
		}
	}

	store := NewInMemoryStore()
	reg := NewRegistry()
	reg.RegisterFunc("w", drainNilWorker)
	drainEnqueue(t, store, "w")

	got, err := Drain(context.Background(), store, reg, newFakeClock(baseTime), DrainOptions{
		Queue:      DefaultQueue,
		Middleware: []Middleware{mw("a"), mw("b")},
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got != (DrainResult{Completed: 1}) {
		t.Fatalf("DrainResult = %+v, want Completed 1", got)
	}
	want := []string{"before:a", "before:b", "after:b", "after:a"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
}

func TestDrainValidation(t *testing.T) {
	store := NewInMemoryStore()
	reg := NewRegistry()
	clock := newFakeClock(baseTime)

	tests := []struct {
		name  string
		store Store
		reg   *Registry
		opts  DrainOptions
	}{
		{name: "nil store", store: nil, reg: reg, opts: DrainOptions{Queue: DefaultQueue}},
		{name: "nil registry", store: store, reg: nil, opts: DrainOptions{Queue: DefaultQueue}},
		{name: "empty queue", store: store, reg: reg, opts: DrainOptions{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Drain(context.Background(), tc.store, tc.reg, clock, tc.opts); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestDrainCancelledContext(t *testing.T) {
	store := NewInMemoryStore()
	reg := NewRegistry()
	reg.RegisterFunc("w", drainNilWorker)
	drainEnqueue(t, store, "w")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Drain(ctx, store, reg, newFakeClock(baseTime), DrainOptions{Queue: DefaultQueue}); err == nil {
		t.Fatalf("expected context error, got nil")
	}
}

func TestDrainQueue(t *testing.T) {
	clock := newFakeClock(baseTime)
	reg := NewRegistry()
	reg.RegisterFunc("w", drainNilWorker)
	store := NewInMemoryStore()

	o, err := New(Config{
		Store:    store,
		Registry: reg,
		Clock:    clock,
		Queues:   map[string]int{DefaultQueue: 1},
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	job, err := NewJob("w", nil)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if _, _, err := o.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, err := DrainQueue(context.Background(), o, DefaultQueue)
	if err != nil {
		t.Fatalf("DrainQueue: %v", err)
	}
	if got != (DrainResult{Completed: 1}) {
		t.Fatalf("DrainResult = %+v, want Completed 1", got)
	}

	if _, err := DrainQueue(context.Background(), nil, DefaultQueue); err == nil {
		t.Fatalf("expected error for nil engine")
	}
}
