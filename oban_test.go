package oban

import (
	"container/heap"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnqueueAndProcess(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(baseTime)
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 3},
		Clock:        clock,
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	type payload struct {
		N int `json:"n"`
	}
	results := make(chan int, 3)
	eng.RegisterFunc("sum", func(_ context.Context, job *Job) error {
		var p payload
		if err := job.UnmarshalArgs(&p); err != nil {
			return err
		}
		results <- p.N
		return nil
	})

	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 1; i <= 3; i++ {
		job := mustJob(t, "sum", payload{N: i})
		stored, inserted, err := eng.Enqueue(ctx, job)
		if err != nil || !inserted {
			t.Fatalf("enqueue: inserted=%v err=%v", inserted, err)
		}
		ids = append(ids, stored.ID)
	}

	got := map[int]bool{}
	for i := 0; i < 3; i++ {
		got[<-results] = true
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if !got[i] {
			t.Errorf("job with n=%d was not processed", i)
		}
	}
	for _, id := range ids {
		j, _ := eng.Store().Get(ctx, id)
		if j.State != StateCompleted {
			t.Errorf("job %d state = %q, want completed", id, j.State)
		}
	}
}

func TestRetryBackoffSchedule(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(baseTime)
	errCh := make(chan *Job, 8)
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 1},
		Clock:        clock,
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
		Backoff:      &ExponentialBackoff{Base: 10 * time.Second, Max: time.Hour, Jitter: 0},
		ErrorHandler: func(job *Job, _ error) { errCh <- job.Clone() },
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.RegisterFunc("always-fail", func(_ context.Context, _ *Job) error {
		return errors.New("boom")
	})

	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _, err := eng.Enqueue(ctx, mustJob(t, "always-fail", nil, WithMaxAttempts(3)))
	if err != nil {
		t.Fatal(err)
	}
	id := stored.ID

	// Attempt 1 fails: retry scheduled at now + 10s.
	<-errCh
	j, _ := eng.Store().Get(ctx, id)
	if j.State != StateRetryable {
		t.Fatalf("after attempt 1 state = %q, want retryable", j.State)
	}
	if want := baseTime.Add(10 * time.Second); !j.ScheduledAt.Equal(want) {
		t.Errorf("retry 1 scheduledAt = %v, want %v", j.ScheduledAt, want)
	}
	if j.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", j.Attempt)
	}

	// Advance to the retry time; attempt 2 fails: retry at now + 20s.
	clock.Set(baseTime.Add(10 * time.Second))
	<-errCh
	j, _ = eng.Store().Get(ctx, id)
	if want := baseTime.Add(30 * time.Second); !j.ScheduledAt.Equal(want) {
		t.Errorf("retry 2 scheduledAt = %v, want %v", j.ScheduledAt, want)
	}
	if j.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", j.Attempt)
	}

	// Advance to attempt 3; exhausts attempts and is discarded.
	clock.Set(baseTime.Add(30 * time.Second))
	<-errCh
	j, _ = eng.Store().Get(ctx, id)
	if j.State != StateDiscarded {
		t.Errorf("after final attempt state = %q, want discarded", j.State)
	}
	if len(j.Errors) != 3 {
		t.Errorf("recorded %d errors, want 3", len(j.Errors))
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(baseTime)
	discarded := make(chan *Job, 1)
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 1},
		Clock:        clock,
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
		ErrorHandler: func(job *Job, _ error) { discarded <- job.Clone() },
	})
	if err != nil {
		t.Fatal(err)
	}
	var attempts int32
	eng.RegisterFunc("fail-once", func(_ context.Context, _ *Job) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("nope")
	})

	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _, err := eng.Enqueue(ctx, mustJob(t, "fail-once", nil, WithMaxAttempts(1)))
	if err != nil {
		t.Fatal(err)
	}
	<-discarded
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	j, _ := eng.Store().Get(ctx, stored.ID)
	if j.State != StateDiscarded {
		t.Errorf("state = %q, want discarded", j.State)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("worker ran %d times, want 1", n)
	}
}

func TestGracefulDrain(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(baseTime)
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 2},
		Clock:        clock,
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var completed int32
	eng.RegisterFunc("slow", func(_ context.Context, _ *Job) error {
		started <- struct{}{}
		<-release // block until the test allows completion
		atomic.AddInt32(&completed, 1)
		return nil
	})

	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _, err := eng.Enqueue(ctx, mustJob(t, "slow", nil))
	if err != nil {
		t.Fatal(err)
	}
	<-started // job is now in flight

	// Begin a graceful stop with a generous drain deadline.
	stopErr := make(chan error, 1)
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopErr <- eng.Stop(stopCtx)
	}()

	// Let the in-flight job finish; Stop must wait for it.
	close(release)
	if err := <-stopErr; err != nil {
		t.Fatalf("graceful Stop returned %v, want nil", err)
	}
	if atomic.LoadInt32(&completed) != 1 {
		t.Error("in-flight job did not complete during drain")
	}
	j, _ := eng.Store().Get(ctx, stored.ID)
	if j.State != StateCompleted {
		t.Errorf("state = %q, want completed", j.State)
	}
}

func TestForcedStopCancelsInFlight(t *testing.T) {
	ctx := context.Background()
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 1},
		Clock:        newFakeClock(baseTime),
		PollInterval: time.Millisecond,
		JobTimeout:   time.Hour,
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	ctxErr := make(chan error, 1)
	eng.RegisterFunc("blocker", func(jobCtx context.Context, _ *Job) error {
		close(started)
		<-jobCtx.Done() // only returns when force-cancelled
		ctxErr <- jobCtx.Err()
		return jobCtx.Err()
	})

	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.Enqueue(ctx, mustJob(t, "blocker", nil)); err != nil {
		t.Fatal(err)
	}
	<-started

	// Stop with an already-expired deadline forces cancellation.
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	err = eng.Stop(stopCtx)
	if err == nil {
		t.Error("forced Stop should return the deadline error")
	}
	if e := <-ctxErr; !errors.Is(e, context.Canceled) {
		t.Errorf("worker context error = %v, want Canceled", e)
	}
}

// TestPeriodicEnqueue drives the cron scheduler's due-firing logic directly so
// the assertion is deterministic and free of real-time waits.
func TestPeriodicEnqueue(t *testing.T) {
	// Start at 12:30:30 so the next minute tick is 12:31:00.
	clock := newFakeClock(time.Date(2026, 7, 17, 12, 30, 30, 0, time.UTC))
	store := NewInMemoryStore()
	eng, err := New(Config{
		Store:  store,
		Queues: map[string]int{DefaultQueue: 1},
		Clock:  clock,
		Logger: discardLogger(),
		Periodic: []Periodic{{
			Schedule: MustParseCron("* * * * *"),
			Worker:   "tick",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.pollCtx = context.Background() // used by fireDue -> enqueuePeriodic

	// Build the scheduler heap exactly as runCron does at startup.
	h := &cronHeap{}
	heap.Push(h, &cronEntry{
		periodic: eng.periodics[0],
		next:     eng.periodics[0].Schedule.Next(clock.Now()),
	})
	heap.Init(h)

	// Nothing is due yet at 12:30:30.
	eng.fireDue(h)
	if n := len(store.All()); n != 0 {
		t.Fatalf("no job should be enqueued before the tick, got %d", n)
	}

	// Advance past the 12:31:00 boundary; the entry becomes due and enqueues.
	clock.Set(time.Date(2026, 7, 17, 12, 31, 5, 0, time.UTC))
	eng.fireDue(h)

	jobs := store.All()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 periodic job, got %d", len(jobs))
	}
	if jobs[0].Worker != "tick" {
		t.Errorf("worker = %q, want tick", jobs[0].Worker)
	}
	// The entry is rescheduled for the following minute.
	if want := time.Date(2026, 7, 17, 12, 32, 0, 0, time.UTC); !(*h)[0].next.Equal(want) {
		t.Errorf("next fire = %v, want %v", (*h)[0].next, want)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{Queues: map[string]int{"bad": 0}}); err == nil {
		t.Error("expected error for non-positive concurrency")
	}
	if _, err := New(Config{Periodic: []Periodic{{Worker: "w"}}}); err == nil {
		t.Error("expected error for nil schedule")
	}
	if _, err := New(Config{Periodic: []Periodic{{Schedule: MustParseCron("* * * * *")}}}); err == nil {
		t.Error("expected error for empty worker")
	}
}

func TestStartTwiceFails(t *testing.T) {
	eng, err := New(Config{Logger: discardLogger(), PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(ctx); err == nil {
		t.Error("second Start should fail")
	}
	_ = eng.Stop(context.Background())
}

func TestUnknownWorkerDiscards(t *testing.T) {
	ctx := context.Background()
	discarded := make(chan *Job, 1)
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 1},
		Clock:        newFakeClock(baseTime),
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
		ErrorHandler: func(job *Job, _ error) { discarded <- job.Clone() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _, err := eng.Enqueue(ctx, mustJob(t, "ghost", nil))
	if err != nil {
		t.Fatal(err)
	}
	<-discarded
	_ = eng.Stop(context.Background())
	j, _ := eng.Store().Get(ctx, stored.ID)
	if j.State != StateDiscarded {
		t.Errorf("state = %q, want discarded", j.State)
	}
}
