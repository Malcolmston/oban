package oban

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mustJob(t *testing.T, worker string, args any, opts ...JobOption) *Job {
	t.Helper()
	j, err := NewJob(worker, args, opts...)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	return j
}

func TestInMemoryStoreEnqueueAssignsID(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	j1 := mustJob(t, "w", nil)
	j1.InsertedAt = baseTime
	got1, inserted, err := s.Enqueue(ctx, j1)
	if err != nil || !inserted {
		t.Fatalf("enqueue 1: inserted=%v err=%v", inserted, err)
	}
	if got1.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if got1.State != StateAvailable {
		t.Errorf("state = %q, want available", got1.State)
	}

	j2 := mustJob(t, "w", nil)
	j2.InsertedAt = baseTime
	got2, _, _ := s.Enqueue(ctx, j2)
	if got2.ID == got1.ID {
		t.Errorf("IDs must be unique, both %d", got1.ID)
	}
}

func TestInMemoryStoreScheduledState(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	future := baseTime.Add(time.Hour)
	j := mustJob(t, "w", nil, WithScheduledAt(future))
	j.InsertedAt = baseTime
	got, _, err := s.Enqueue(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateScheduled {
		t.Errorf("state = %q, want scheduled", got.State)
	}
	if !got.ScheduledAt.Equal(future) {
		t.Errorf("scheduledAt = %v, want %v", got.ScheduledAt, future)
	}
}

func TestInMemoryStoreScheduleIn(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := mustJob(t, "w", nil, WithScheduleIn(time.Minute))
	j.InsertedAt = baseTime
	got, _, err := s.Enqueue(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	want := baseTime.Add(time.Minute)
	if !got.ScheduledAt.Equal(want) {
		t.Errorf("scheduledAt = %v, want %v", got.ScheduledAt, want)
	}
	if got.State != StateScheduled {
		t.Errorf("state = %q, want scheduled", got.State)
	}
}

func TestFetchAvailablePriorityAndScheduledOrdering(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	// Insert in a deliberately shuffled order.
	specs := []struct {
		label     string
		priority  int
		scheduled time.Time
	}{
		{"p1-late", 1, baseTime.Add(-1 * time.Minute)},
		{"p0-late", 0, baseTime.Add(-1 * time.Minute)},
		{"p0-early", 0, baseTime.Add(-5 * time.Minute)},
		{"p2-early", 2, baseTime.Add(-5 * time.Minute)},
	}
	for _, sp := range specs {
		j := mustJob(t, "w", nil, WithPriority(sp.priority), WithScheduledAt(sp.scheduled))
		j.InsertedAt = baseTime.Add(-10 * time.Minute)
		if _, _, err := s.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.FetchAvailable(ctx, DefaultQueue, 10, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	// Expected order: priority asc, then scheduledAt asc.
	want := []struct {
		priority  int
		scheduled time.Time
	}{
		{0, baseTime.Add(-5 * time.Minute)},
		{0, baseTime.Add(-1 * time.Minute)},
		{1, baseTime.Add(-1 * time.Minute)},
		{2, baseTime.Add(-5 * time.Minute)},
	}
	if len(got) != len(want) {
		t.Fatalf("fetched %d jobs, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Priority != w.priority || !got[i].ScheduledAt.Equal(w.scheduled) {
			t.Errorf("position %d: got priority=%d sched=%v, want priority=%d sched=%v",
				i, got[i].Priority, got[i].ScheduledAt, w.priority, w.scheduled)
		}
		if got[i].State != StateExecuting {
			t.Errorf("fetched job %d state = %q, want executing", i, got[i].State)
		}
		if got[i].Attempt != 1 {
			t.Errorf("fetched job %d attempt = %d, want 1", i, got[i].Attempt)
		}
	}
}

func TestFetchAvailableRespectsScheduleAndLimit(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	// A future job must not be fetched.
	future := mustJob(t, "w", nil, WithScheduledAt(baseTime.Add(time.Hour)))
	future.InsertedAt = baseTime
	if _, _, err := s.Enqueue(ctx, future); err != nil {
		t.Fatal(err)
	}
	// Two ready jobs.
	for i := 0; i < 2; i++ {
		j := mustJob(t, "w", nil)
		j.InsertedAt = baseTime.Add(-time.Minute)
		if _, _, err := s.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.FetchAvailable(ctx, DefaultQueue, 1, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("limit not honored: fetched %d, want 1", len(got))
	}

	// Only one ready job remains; the future one stays hidden.
	got, _ = s.FetchAvailable(ctx, DefaultQueue, 10, baseTime)
	if len(got) != 1 {
		t.Fatalf("fetched %d, want 1 remaining ready job", len(got))
	}
	if s.CountByState(StateScheduled) != 1 {
		t.Errorf("scheduled count = %d, want 1", s.CountByState(StateScheduled))
	}
}

func TestFetchAvailableNoDoubleDispatch(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := mustJob(t, "w", nil)
	j.InsertedAt = baseTime
	if _, _, err := s.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	first, _ := s.FetchAvailable(ctx, DefaultQueue, 10, baseTime)
	second, _ := s.FetchAvailable(ctx, DefaultQueue, 10, baseTime)
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("expected single dispatch, got first=%d second=%d", len(first), len(second))
	}
}

func TestTransitionsCompleteRetryDiscard(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := mustJob(t, "w", nil, WithMaxAttempts(3))
	j.InsertedAt = baseTime
	stored, _, _ := s.Enqueue(ctx, j)
	id := stored.ID

	if _, err := s.FetchAvailable(ctx, DefaultQueue, 1, baseTime); err != nil {
		t.Fatal(err)
	}

	// Retry.
	retryAt := baseTime.Add(10 * time.Second)
	if err := s.Retry(ctx, id, retryAt, errors.New("boom"), baseTime); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id)
	if got.State != StateRetryable {
		t.Errorf("state = %q, want retryable", got.State)
	}
	if !got.ScheduledAt.Equal(retryAt) {
		t.Errorf("scheduledAt = %v, want %v", got.ScheduledAt, retryAt)
	}
	if got.LastError != "boom" || len(got.Errors) != 1 {
		t.Errorf("error history = %+v, LastError=%q", got.Errors, got.LastError)
	}

	// Complete.
	if err := s.Complete(ctx, id, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, id)
	if got.State != StateCompleted || got.CompletedAt.IsZero() {
		t.Errorf("state = %q completedAt = %v", got.State, got.CompletedAt)
	}

	// Discard.
	if err := s.Discard(ctx, id, errors.New("dead"), baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, id)
	if got.State != StateDiscarded || got.DiscardedAt.IsZero() {
		t.Errorf("state = %q discardedAt = %v", got.State, got.DiscardedAt)
	}
	if len(got.Errors) != 2 {
		t.Errorf("expected 2 recorded errors, got %d", len(got.Errors))
	}
}

func TestStoreErrJobNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	if _, err := s.Get(ctx, 999); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Get: got %v, want ErrJobNotFound", err)
	}
	if err := s.Complete(ctx, 999, baseTime); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Complete: got %v, want ErrJobNotFound", err)
	}
}

func TestUniqueDeduplication(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	first := mustJob(t, "mailer", map[string]int{"user": 1}, WithUnique("welcome-1", time.Hour))
	first.InsertedAt = baseTime
	stored, inserted, err := s.Enqueue(ctx, first)
	if err != nil || !inserted {
		t.Fatalf("first enqueue: inserted=%v err=%v", inserted, err)
	}

	// Duplicate within the period is skipped and returns the existing job.
	dup := mustJob(t, "mailer", map[string]int{"user": 1}, WithUnique("welcome-1", time.Hour))
	dup.InsertedAt = baseTime.Add(30 * time.Minute)
	got, inserted, err := s.Enqueue(ctx, dup)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Error("duplicate within period should not insert")
	}
	if got.ID != stored.ID {
		t.Errorf("dedup returned ID %d, want existing %d", got.ID, stored.ID)
	}

	// A different key is not de-duplicated.
	other := mustJob(t, "mailer", nil, WithUnique("welcome-2", time.Hour))
	other.InsertedAt = baseTime.Add(30 * time.Minute)
	if _, inserted, _ := s.Enqueue(ctx, other); !inserted {
		t.Error("different unique key should insert")
	}

	// Outside the period, a new job is inserted.
	late := mustJob(t, "mailer", nil, WithUnique("welcome-1", time.Hour))
	late.InsertedAt = baseTime.Add(2 * time.Hour)
	if _, inserted, _ := s.Enqueue(ctx, late); !inserted {
		t.Error("beyond unique period should insert")
	}
}

func TestUniqueIgnoresFinishedJobs(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	first := mustJob(t, "w", nil, WithUnique("k", time.Hour))
	first.InsertedAt = baseTime
	stored, _, _ := s.Enqueue(ctx, first)

	// Complete the first job; it no longer blocks a duplicate.
	if _, err := s.FetchAvailable(ctx, DefaultQueue, 1, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, stored.ID, baseTime); err != nil {
		t.Fatal(err)
	}

	dup := mustJob(t, "w", nil, WithUnique("k", time.Hour))
	dup.InsertedAt = baseTime.Add(time.Minute)
	if _, inserted, _ := s.Enqueue(ctx, dup); !inserted {
		t.Error("completed job should not block a new unique job")
	}
}
