package oban

import (
	"context"
	"testing"
	"time"
)

// enqueueForTest inserts a job and returns its stored copy.
func enqueueForTest(t *testing.T, s *InMemoryStore, worker string, opts ...JobOption) *Job {
	t.Helper()
	j, err := NewJob(worker, map[string]string{"k": "v"}, opts...)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	j.InsertedAt = baseTime
	stored, _, err := s.Enqueue(context.Background(), j)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return stored
}

func TestInMemoryCancel(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := enqueueForTest(t, s, "w")

	if err := s.Cancel(ctx, j.ID, baseTime); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ := s.Get(ctx, j.ID)
	if got.State != StateCancelled {
		t.Errorf("state = %q, want cancelled", got.State)
	}
	if !got.DiscardedAt.Equal(baseTime) {
		t.Errorf("DiscardedAt = %v, want %v", got.DiscardedAt, baseTime)
	}

	// Cancelling a finished job is a no-op that keeps the terminal state.
	_ = s.Complete(ctx, j.ID, baseTime) // still cancelled? Complete overwrites, so use fresh job.
	j2 := enqueueForTest(t, s, "w2")
	_ = s.Complete(ctx, j2.ID, baseTime)
	if err := s.Cancel(ctx, j2.ID, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("Cancel completed: %v", err)
	}
	got2, _ := s.Get(ctx, j2.ID)
	if got2.State != StateCompleted {
		t.Errorf("completed job changed to %q after Cancel", got2.State)
	}

	if err := s.Cancel(ctx, 9999, baseTime); err != ErrJobNotFound {
		t.Errorf("Cancel missing: got %v, want ErrJobNotFound", err)
	}
}

func TestInMemoryDelete(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := enqueueForTest(t, s, "w")
	_ = s.SetTagsMeta(ctx, j.ID, []string{"a"}, map[string]any{"x": 1})

	if err := s.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, j.ID); err != ErrJobNotFound {
		t.Errorf("Get after delete: %v", err)
	}
	tags, _, _ := s.TagsMeta(ctx, j.ID)
	if tags != nil {
		t.Errorf("tags survived delete: %v", tags)
	}
	// Deleting a missing id is a no-op.
	if err := s.Delete(ctx, 9999); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestInMemoryRetryNow(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := enqueueForTest(t, s, "w")
	_ = s.Discard(ctx, j.ID, context.DeadlineExceeded, baseTime)

	at := baseTime.Add(time.Hour)
	if err := s.RetryNow(ctx, j.ID, at); err != nil {
		t.Fatalf("RetryNow: %v", err)
	}
	got, _ := s.Get(ctx, j.ID)
	if got.State != StateAvailable {
		t.Errorf("state = %q, want available", got.State)
	}
	if !got.ScheduledAt.Equal(at) {
		t.Errorf("ScheduledAt = %v, want %v", got.ScheduledAt, at)
	}
	if !got.DiscardedAt.IsZero() {
		t.Errorf("DiscardedAt not cleared: %v", got.DiscardedAt)
	}
}

func TestInMemorySnooze(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := enqueueForTest(t, s, "w")
	// Move it to executing via a fetch so Attempt increments.
	fetched, _ := s.FetchAvailable(ctx, DefaultQueue, 1, baseTime)
	if len(fetched) != 1 {
		t.Fatalf("expected 1 fetched, got %d", len(fetched))
	}
	if fetched[0].Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", fetched[0].Attempt)
	}

	until := baseTime.Add(5 * time.Minute)
	if err := s.Snooze(ctx, j.ID, until, baseTime); err != nil {
		t.Fatalf("Snooze: %v", err)
	}
	got, _ := s.Get(ctx, j.ID)
	if got.State != StateScheduled {
		t.Errorf("state = %q, want scheduled", got.State)
	}
	if !got.ScheduledAt.Equal(until) {
		t.Errorf("ScheduledAt = %v, want %v", got.ScheduledAt, until)
	}
	if got.Attempt != 0 {
		t.Errorf("attempt = %d, want 0 (snooze refunds)", got.Attempt)
	}

	// Snoozing a non-executing job is a no-op.
	if err := s.Snooze(ctx, j.ID, until, baseTime); err != nil {
		t.Fatalf("Snooze non-executing: %v", err)
	}
}

func TestInMemoryDeleteFinishedBefore(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	// Two completed jobs: one old, one recent.
	old := enqueueForTest(t, s, "w")
	recent := enqueueForTest(t, s, "w")
	_ = s.Complete(ctx, old.ID, baseTime.Add(-2*time.Hour))
	_ = s.Complete(ctx, recent.ID, baseTime)
	// An available job must never be pruned.
	_ = enqueueForTest(t, s, "w")

	cutoff := baseTime.Add(-time.Hour)
	n, err := s.DeleteFinishedBefore(ctx, []State{StateCompleted}, cutoff, 0)
	if err != nil {
		t.Fatalf("DeleteFinishedBefore: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	if _, err := s.Get(ctx, old.ID); err != ErrJobNotFound {
		t.Errorf("old job should be gone: %v", err)
	}
	if _, err := s.Get(ctx, recent.ID); err != nil {
		t.Errorf("recent job should survive: %v", err)
	}
}

func TestInMemoryDeleteFinishedBeforeLimit(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	for i := 0; i < 5; i++ {
		j := enqueueForTest(t, s, "w")
		_ = s.Complete(ctx, j.ID, baseTime.Add(-2*time.Hour))
	}
	n, err := s.DeleteFinishedBefore(ctx, []State{StateCompleted}, baseTime, 2)
	if err != nil {
		t.Fatalf("DeleteFinishedBefore: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2 (limit)", n)
	}
	if s.Len() != 3 {
		t.Errorf("remaining %d, want 3", s.Len())
	}
}

func TestInMemoryRescueExecuting(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	// A retryable-capacity job stuck executing gets rescued to available.
	live := enqueueForTest(t, s, "w", WithMaxAttempts(3))
	// A job on its last attempt gets discarded.
	last := enqueueForTest(t, s, "w", WithMaxAttempts(1))

	fetched, _ := s.FetchAvailable(ctx, DefaultQueue, 10, baseTime)
	if len(fetched) != 2 {
		t.Fatalf("fetched %d, want 2", len(fetched))
	}

	olderThan := baseTime.Add(time.Minute)
	now := baseTime.Add(2 * time.Minute)
	rescued, discarded, err := s.RescueExecuting(ctx, olderThan, now)
	if err != nil {
		t.Fatalf("RescueExecuting: %v", err)
	}
	if rescued != 1 || discarded != 1 {
		t.Fatalf("rescued=%d discarded=%d, want 1 and 1", rescued, discarded)
	}
	gl, _ := s.Get(ctx, live.ID)
	if gl.State != StateAvailable || !gl.ScheduledAt.Equal(now) {
		t.Errorf("live job: state=%q scheduled=%v", gl.State, gl.ScheduledAt)
	}
	gd, _ := s.Get(ctx, last.ID)
	if gd.State != StateDiscarded {
		t.Errorf("last job: state=%q, want discarded", gd.State)
	}
}

func TestInMemoryFindConflict(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := enqueueForTest(t, s, "w")

	by := UniqueBy{Period: time.Hour}
	// A logically-identical job conflicts.
	dup, _ := NewJob("w", map[string]string{"k": "v"})
	dup.InsertedAt = baseTime
	conflict, err := s.FindConflict(ctx, dup, by, baseTime)
	if err != nil {
		t.Fatalf("FindConflict: %v", err)
	}
	if conflict == nil || conflict.ID != j.ID {
		t.Fatalf("expected conflict with job %d, got %v", j.ID, conflict)
	}

	// A different worker does not conflict.
	other, _ := NewJob("other", map[string]string{"k": "v"})
	other.InsertedAt = baseTime
	if c, _ := s.FindConflict(ctx, other, by, baseTime); c != nil {
		t.Errorf("unexpected conflict for different worker: %v", c)
	}

	// Outside the period there is no conflict.
	if c, _ := s.FindConflict(ctx, dup, by, baseTime.Add(2*time.Hour)); c != nil {
		t.Errorf("unexpected conflict outside period: %v", c)
	}

	// A finished job does not block.
	_ = s.Complete(ctx, j.ID, baseTime)
	if c, _ := s.FindConflict(ctx, dup, by, baseTime); c != nil {
		t.Errorf("completed job should not conflict: %v", c)
	}
}

func TestInMemoryFindConflictThroughInsert(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	j1, _ := NewJob("w", map[string]string{"a": "1"})
	j1.InsertedAt = baseTime
	_, ins1, err := Insert(ctx, s, j1, InsertOpts{Unique: &UniqueBy{Period: time.Hour}})
	if err != nil || !ins1 {
		t.Fatalf("first insert: inserted=%v err=%v", ins1, err)
	}

	j2, _ := NewJob("w", map[string]string{"a": "1"})
	j2.InsertedAt = baseTime
	res, ins2, err := Insert(ctx, s, j2, InsertOpts{Unique: &UniqueBy{Period: time.Hour}})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if ins2 {
		t.Fatal("second insert should have been de-duplicated")
	}
	if res.ID != 1 {
		t.Errorf("conflict id = %d, want 1", res.ID)
	}
	if s.Len() != 1 {
		t.Errorf("store has %d jobs, want 1", s.Len())
	}
}

func TestInMemoryTagsMeta(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j := enqueueForTest(t, s, "w")

	tags := []string{"urgent", "email"}
	meta := map[string]any{"tenant": "acme", "n": 3}
	if err := s.SetTagsMeta(ctx, j.ID, tags, meta); err != nil {
		t.Fatalf("SetTagsMeta: %v", err)
	}

	gotTags, gotMeta, err := s.TagsMeta(ctx, j.ID)
	if err != nil {
		t.Fatalf("TagsMeta: %v", err)
	}
	if len(gotTags) != 2 || gotTags[0] != "urgent" || gotTags[1] != "email" {
		t.Errorf("tags = %v", gotTags)
	}
	if gotMeta["tenant"] != "acme" || gotMeta["n"] != 3 {
		t.Errorf("meta = %v", gotMeta)
	}

	// Returned values are copies: mutating them must not affect storage.
	gotTags[0] = "changed"
	gotMeta["tenant"] = "changed"
	reTags, reMeta, _ := s.TagsMeta(ctx, j.ID)
	if reTags[0] != "urgent" || reMeta["tenant"] != "acme" {
		t.Error("stored tags/meta were mutated through returned copies")
	}

	// The package-level Tags helper reads them back too.
	helperTags, _ := Tags(ctx, s, j.ID)
	if len(helperTags) != 2 {
		t.Errorf("Tags helper returned %v", helperTags)
	}
}

// Verify the store is usable with the higher-level Controller and Pruner that
// require the optional capabilities, which was the point of adding them.
func TestInMemoryImplementsControlPlane(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	clock := newFakeClock(baseTime)
	ctrl := NewController(s, clock)

	j := enqueueForTest(t, s, "w")
	if err := ctrl.Cancel(ctx, j.ID); err != nil {
		t.Fatalf("controller cancel: %v", err)
	}
	got, _ := s.Get(ctx, j.ID)
	if got.State != StateCancelled {
		t.Errorf("state = %q, want cancelled", got.State)
	}
	if err := ctrl.Retry(ctx, j.ID); err != nil {
		t.Fatalf("controller retry: %v", err)
	}
	if err := ctrl.Delete(ctx, j.ID); err != nil {
		t.Fatalf("controller delete: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("store not empty after delete: %d", s.Len())
	}
}
