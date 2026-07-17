package oban

import (
	"context"
	"errors"
	"testing"
	"time"
)

// controlCapStore is a minimal in-memory [Store] used by the control tests that
// also implements the optional Cancel/RetryNow/Delete capabilities. It records
// each capability call so tests can assert the Controller reached the store.
type controlCapStore struct {
	jobs        map[int64]*Job
	cancelCalls []int64
	retryCalls  []int64
	deleteCalls []int64
}

func newControlCapStore(jobs ...*Job) *controlCapStore {
	s := &controlCapStore{jobs: make(map[int64]*Job)}
	for _, j := range jobs {
		s.jobs[j.ID] = j.Clone()
	}
	return s
}

func (s *controlCapStore) Enqueue(_ context.Context, job *Job) (*Job, bool, error) {
	s.jobs[job.ID] = job.Clone()
	return job.Clone(), true, nil
}

func (s *controlCapStore) FetchAvailable(_ context.Context, _ string, _ int, _ time.Time) ([]*Job, error) {
	return nil, nil
}

func (s *controlCapStore) Complete(_ context.Context, _ int64, _ time.Time) error { return nil }

func (s *controlCapStore) Retry(_ context.Context, _ int64, _ time.Time, _ error, _ time.Time) error {
	return nil
}

func (s *controlCapStore) Discard(_ context.Context, _ int64, _ error, _ time.Time) error {
	return nil
}

func (s *controlCapStore) Get(_ context.Context, id int64) (*Job, error) {
	j, ok := s.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	return j.Clone(), nil
}

func (s *controlCapStore) Cancel(_ context.Context, id int64, now time.Time) error {
	s.cancelCalls = append(s.cancelCalls, id)
	if j, ok := s.jobs[id]; ok {
		j.State = StateCancelled
		j.DiscardedAt = now
	}
	return nil
}

func (s *controlCapStore) RetryNow(_ context.Context, id int64, now time.Time) error {
	s.retryCalls = append(s.retryCalls, id)
	if j, ok := s.jobs[id]; ok {
		j.State = StateAvailable
		j.ScheduledAt = now
	}
	return nil
}

func (s *controlCapStore) Delete(_ context.Context, id int64) error {
	s.deleteCalls = append(s.deleteCalls, id)
	delete(s.jobs, id)
	return nil
}

// controlSeed enqueues n available jobs on the given queue in store and returns
// their IDs in insertion order.
func controlSeed(t *testing.T, store Store, queue string, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		job, err := NewJob("worker", nil, WithQueue(queue))
		if err != nil {
			t.Fatalf("NewJob: %v", err)
		}
		job.InsertedAt = baseTime
		stored, _, err := store.Enqueue(context.Background(), job)
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		ids = append(ids, stored.ID)
	}
	return ids
}

func TestControlledStorePauseResume(t *testing.T) {
	ctx := context.Background()
	inner := NewInMemoryStore()
	cs := NewControlledStore(inner)
	controlSeed(t, cs, "q", 3)

	if cs.IsPaused("q") {
		t.Fatal("queue should start unpaused")
	}

	// Unpaused: work flows through to the inner store.
	got, err := cs.FetchAvailable(ctx, "q", 10, baseTime)
	if err != nil {
		t.Fatalf("FetchAvailable: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("unpaused fetch = %d jobs, want 3", len(got))
	}

	// Re-seed and pause: fetch yields nothing and does not touch the inner store.
	inner2 := NewInMemoryStore()
	cs2 := NewControlledStore(inner2)
	controlSeed(t, cs2, "q", 3)
	cs2.Pause("q")

	if !cs2.IsPaused("q") {
		t.Fatal("IsPaused = false after Pause, want true")
	}
	got, err = cs2.FetchAvailable(ctx, "q", 10, baseTime)
	if err != nil {
		t.Fatalf("FetchAvailable (paused): %v", err)
	}
	if got != nil {
		t.Fatalf("paused fetch = %v, want nil", got)
	}
	if n := inner2.CountByState(StateExecuting); n != 0 {
		t.Fatalf("paused fetch transitioned %d jobs, want 0", n)
	}

	// A different queue is unaffected by pausing "q".
	controlSeed(t, cs2, "other", 1)
	got, err = cs2.FetchAvailable(ctx, "other", 10, baseTime)
	if err != nil {
		t.Fatalf("FetchAvailable (other): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("other queue fetch = %d, want 1", len(got))
	}

	// Resume restores flow.
	cs2.Resume("q")
	if cs2.IsPaused("q") {
		t.Fatal("IsPaused = true after Resume, want false")
	}
	got, err = cs2.FetchAvailable(ctx, "q", 10, baseTime)
	if err != nil {
		t.Fatalf("FetchAvailable (resumed): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("resumed fetch = %d jobs, want 3", len(got))
	}
}

func TestControlledStoreScale(t *testing.T) {
	tests := []struct {
		name     string
		cap      int
		limit    int
		wantSeed int
		wantN    int
	}{
		{name: "cap below limit", cap: 2, limit: 10, wantSeed: 5, wantN: 2},
		{name: "cap above limit", cap: 8, limit: 3, wantSeed: 5, wantN: 3},
		{name: "cap equals limit", cap: 4, limit: 4, wantSeed: 5, wantN: 4},
		{name: "cap removed by zero", cap: 0, limit: 10, wantSeed: 5, wantN: 5},
		{name: "cap removed by negative", cap: -1, limit: 10, wantSeed: 5, wantN: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs := NewControlledStore(NewInMemoryStore())
			controlSeed(t, cs, "q", tt.wantSeed)
			cs.Scale("q", tt.cap)

			got, err := cs.FetchAvailable(ctx, "q", tt.limit, baseTime)
			if err != nil {
				t.Fatalf("FetchAvailable: %v", err)
			}
			if len(got) != tt.wantN {
				t.Fatalf("fetched %d jobs, want %d", len(got), tt.wantN)
			}
		})
	}
}

func TestControlledStoreDelegates(t *testing.T) {
	ctx := context.Background()
	inner := NewInMemoryStore()
	cs := NewControlledStore(inner)
	ids := controlSeed(t, cs, "q", 1)
	id := ids[0]

	// Get delegates.
	if _, err := cs.Get(ctx, id); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := cs.Get(ctx, 9999); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrJobNotFound", err)
	}

	// Fetch (to executing) then Complete/Retry/Discard delegate to the inner store.
	if _, err := cs.FetchAvailable(ctx, "q", 1, baseTime); err != nil {
		t.Fatalf("FetchAvailable: %v", err)
	}
	if err := cs.Complete(ctx, id, baseTime); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if n := inner.CountByState(StateCompleted); n != 1 {
		t.Fatalf("CountByState(completed) = %d, want 1", n)
	}
}

func TestControllerUnsupportedStore(t *testing.T) {
	ctx := context.Background()
	// InMemoryStore implements none of the optional capabilities.
	store := NewInMemoryStore()
	ids := controlSeed(t, store, "q", 1)
	ctrl := NewController(store, newFakeClock(baseTime))

	ops := map[string]func(int64) error{
		"cancel": func(id int64) error { return ctrl.Cancel(ctx, id) },
		"retry":  func(id int64) error { return ctrl.Retry(ctx, id) },
		"delete": func(id int64) error { return ctrl.Delete(ctx, id) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			if err := op(ids[0]); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("%s on unsupported store = %v, want ErrUnsupported", name, err)
			}
		})
	}
}

func TestControllerJobControl(t *testing.T) {
	clk := newFakeClock(baseTime)
	later := baseTime.Add(time.Hour)

	tests := []struct {
		name      string
		op        func(ctrl *Controller, id int64) error
		wantState State
		calls     func(s *controlCapStore) []int64
	}{
		{
			name:      "cancel sets cancelled",
			op:        func(ctrl *Controller, id int64) error { return ctrl.Cancel(context.Background(), id) },
			wantState: StateCancelled,
			calls:     func(s *controlCapStore) []int64 { return s.cancelCalls },
		},
		{
			name:      "retry sets available",
			op:        func(ctrl *Controller, id int64) error { return ctrl.Retry(context.Background(), id) },
			wantState: StateAvailable,
			calls:     func(s *controlCapStore) []int64 { return s.retryCalls },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk.Set(later)
			job := &Job{ID: 7, Queue: "q", Worker: "w", State: StateDiscarded}
			store := newControlCapStore(job)
			ctrl := NewController(store, clk)

			if err := tt.op(ctrl, 7); err != nil {
				t.Fatalf("op: %v", err)
			}
			if got := tt.calls(store); len(got) != 1 || got[0] != 7 {
				t.Fatalf("capability calls = %v, want [7]", got)
			}
			after, err := store.Get(context.Background(), 7)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if after.State != tt.wantState {
				t.Fatalf("state = %q, want %q", after.State, tt.wantState)
			}
		})
	}

	t.Run("delete removes job", func(t *testing.T) {
		job := &Job{ID: 7, Queue: "q", Worker: "w", State: StateDiscarded}
		store := newControlCapStore(job)
		ctrl := NewController(store, clk)

		if err := ctrl.Delete(context.Background(), 7); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(store.deleteCalls) != 1 || store.deleteCalls[0] != 7 {
			t.Fatalf("deleteCalls = %v, want [7]", store.deleteCalls)
		}
		if _, err := store.Get(context.Background(), 7); !errors.Is(err, ErrJobNotFound) {
			t.Fatalf("Get after delete = %v, want ErrJobNotFound", err)
		}
	})
}

func TestControllerMissingJob(t *testing.T) {
	clk := newFakeClock(baseTime)
	store := newControlCapStore() // capabilities supported, but no jobs
	ctrl := NewController(store, clk)

	ops := map[string]func(int64) error{
		"cancel": func(id int64) error { return ctrl.Cancel(context.Background(), id) },
		"retry":  func(id int64) error { return ctrl.Retry(context.Background(), id) },
		"delete": func(id int64) error { return ctrl.Delete(context.Background(), id) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			if err := op(42); !errors.Is(err, ErrJobNotFound) {
				t.Fatalf("%s(missing) = %v, want ErrJobNotFound", name, err)
			}
		})
	}

	// The capability must not be invoked when the job is absent.
	if len(store.cancelCalls)+len(store.retryCalls)+len(store.deleteCalls) != 0 {
		t.Fatalf("capabilities invoked for missing job: cancel=%v retry=%v delete=%v",
			store.cancelCalls, store.retryCalls, store.deleteCalls)
	}
}

func TestControllerNilClockDefaults(t *testing.T) {
	store := newControlCapStore(&Job{ID: 1, Queue: "q", Worker: "w", State: StateDiscarded})
	ctrl := NewController(store, nil)
	if err := ctrl.Cancel(context.Background(), 1); err != nil {
		t.Fatalf("Cancel with nil clock: %v", err)
	}
	if len(store.cancelCalls) != 1 {
		t.Fatalf("cancelCalls = %v, want one call", store.cancelCalls)
	}
}
