package oban

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// worker_configTestWorker is a [ConfiguredWorker] whose Perform returns a fixed
// error, used to drive [WorkerMiddleware] deterministically.
type worker_configTestWorker struct {
	opts WorkerOptions
	err  error
}

func (w worker_configTestWorker) Perform(context.Context, *Job) error { return w.err }
func (w worker_configTestWorker) Config() WorkerOptions               { return w.opts }

// worker_configConstBackoff is a [Backoff] that always returns the same delay.
type worker_configConstBackoff time.Duration

func (b worker_configConstBackoff) Next(int) time.Duration { return time.Duration(b) }

// worker_configSnoozeCall records the arguments of a Snooze invocation.
type worker_configSnoozeCall struct {
	id    int64
	until time.Time
	now   time.Time
}

// worker_configRetryCall records the arguments of a RetryNow invocation.
type worker_configRetryCall struct {
	id  int64
	now time.Time
}

// worker_configCapStore is a [Store] that also implements [SnoozableStore],
// [CancelableStore] and [RetryableStore], recording every capability call.
type worker_configCapStore struct {
	*InMemoryStore
	snoozes []worker_configSnoozeCall
	cancels []int64
	retries []worker_configRetryCall
}

func worker_configNewCapStore() *worker_configCapStore {
	return &worker_configCapStore{InMemoryStore: NewInMemoryStore()}
}

func (s *worker_configCapStore) Snooze(_ context.Context, id int64, until, now time.Time) error {
	s.snoozes = append(s.snoozes, worker_configSnoozeCall{id: id, until: until, now: now})
	return nil
}

func (s *worker_configCapStore) Cancel(_ context.Context, id int64, _ time.Time) error {
	s.cancels = append(s.cancels, id)
	return nil
}

func (s *worker_configCapStore) RetryNow(_ context.Context, id int64, now time.Time) error {
	s.retries = append(s.retries, worker_configRetryCall{id: id, now: now})
	return nil
}

func TestBuildJob(t *testing.T) {
	cfg := WorkerOptions{Queue: "mailers", MaxAttempts: 5, Priority: 3}

	tests := []struct {
		name      string
		worker    Worker
		opts      []JobOption
		wantQueue string
		wantMax   int
		wantPrio  int
	}{
		{
			name:      "plain worker keeps package defaults",
			worker:    WorkerFunc(func(context.Context, *Job) error { return nil }),
			wantQueue: DefaultQueue,
			wantMax:   DefaultMaxAttempts,
			wantPrio:  0,
		},
		{
			name:      "configured worker supplies defaults",
			worker:    worker_configTestWorker{opts: cfg},
			wantQueue: "mailers",
			wantMax:   5,
			wantPrio:  3,
		},
		{
			name:      "caller options override worker defaults",
			worker:    worker_configTestWorker{opts: cfg},
			opts:      []JobOption{WithQueue("urgent"), WithPriority(1)},
			wantQueue: "urgent",
			wantMax:   5, // still from config
			wantPrio:  1,
		},
		{
			name:      "nil worker is safe",
			worker:    nil,
			wantQueue: DefaultQueue,
			wantMax:   DefaultMaxAttempts,
			wantPrio:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job, err := BuildJob("emailer", tc.worker, map[string]int{"id": 1}, tc.opts...)
			if err != nil {
				t.Fatalf("BuildJob: %v", err)
			}
			if job.Queue != tc.wantQueue {
				t.Errorf("Queue = %q, want %q", job.Queue, tc.wantQueue)
			}
			if job.MaxAttempts != tc.wantMax {
				t.Errorf("MaxAttempts = %d, want %d", job.MaxAttempts, tc.wantMax)
			}
			if job.Priority != tc.wantPrio {
				t.Errorf("Priority = %d, want %d", job.Priority, tc.wantPrio)
			}
			if job.Worker != "emailer" {
				t.Errorf("Worker = %q, want %q", job.Worker, "emailer")
			}
		})
	}
}

func TestSnoozeCancelSignals(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantSnooze bool
		wantDur    time.Duration
		wantCancel bool
		wantReason string
	}{
		{
			name:       "snooze",
			err:        Snooze(2 * time.Second),
			wantSnooze: true,
			wantDur:    2 * time.Second,
		},
		{
			name:       "cancel",
			err:        CancelJob("boom"),
			wantCancel: true,
			wantReason: "boom",
		},
		{
			name:       "wrapped snooze",
			err:        fmt.Errorf("context: %w", Snooze(3*time.Second)),
			wantSnooze: true,
			wantDur:    3 * time.Second,
		},
		{
			name:       "wrapped cancel",
			err:        fmt.Errorf("context: %w", CancelJob("stop")),
			wantCancel: true,
			wantReason: "stop",
		},
		{
			name: "plain error is neither",
			err:  errors.New("plain"),
		},
		{
			name: "nil error is neither",
			err:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotDur, gotSnooze := IsSnooze(tc.err)
			if gotSnooze != tc.wantSnooze {
				t.Errorf("IsSnooze ok = %v, want %v", gotSnooze, tc.wantSnooze)
			}
			if gotSnooze && gotDur != tc.wantDur {
				t.Errorf("IsSnooze dur = %v, want %v", gotDur, tc.wantDur)
			}
			gotReason, gotCancel := IsCancel(tc.err)
			if gotCancel != tc.wantCancel {
				t.Errorf("IsCancel ok = %v, want %v", gotCancel, tc.wantCancel)
			}
			if gotCancel && gotReason != tc.wantReason {
				t.Errorf("IsCancel reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

func TestSignalErrorStrings(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"snooze", SnoozeError{For: time.Second}, "oban: snooze for 1s"},
		{"cancel with reason", CancelError{Reason: "x"}, "oban: job cancelled: x"},
		{"cancel without reason", CancelError{}, "oban: job cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkerMiddlewareResults(t *testing.T) {
	const jobID = int64(42)
	clk := newFakeClock(baseTime)

	tests := []struct {
		name string
		// worker is placed in the attempt context; nil means none.
		worker Worker
		// bare selects a plain store lacking the optional capabilities.
		bare bool
		perr error

		wantErr     bool
		wantSnoozes int
		wantCancels int
		wantRetries int
		wantUntil   time.Time
		wantRetryAt time.Time
	}{
		{
			name:        "snooze reschedules and swallows error",
			worker:      worker_configTestWorker{},
			perr:        Snooze(5 * time.Second),
			wantSnoozes: 1,
			wantUntil:   baseTime.Add(5 * time.Second),
		},
		{
			name:        "snooze works without worker in context",
			worker:      nil,
			perr:        Snooze(time.Minute),
			wantSnoozes: 1,
			wantUntil:   baseTime.Add(time.Minute),
		},
		{
			name:        "cancel moves job and swallows error",
			worker:      worker_configTestWorker{},
			perr:        CancelJob("done"),
			wantCancels: 1,
		},
		{
			name:    "snooze without capability passes error through",
			worker:  worker_configTestWorker{},
			bare:    true,
			perr:    Snooze(time.Second),
			wantErr: true,
		},
		{
			name:    "cancel without capability passes error through",
			worker:  worker_configTestWorker{},
			bare:    true,
			perr:    CancelJob("x"),
			wantErr: true,
		},
		{
			name:        "error with per-worker backoff reschedules",
			worker:      worker_configTestWorker{opts: WorkerOptions{Backoff: worker_configConstBackoff(10 * time.Second)}},
			perr:        errors.New("boom"),
			wantRetries: 1,
			wantRetryAt: baseTime.Add(10 * time.Second),
		},
		{
			name:    "error without per-worker backoff passes through",
			worker:  worker_configTestWorker{},
			perr:    errors.New("boom"),
			wantErr: true,
		},
		{
			name:    "error with unconfigured worker passes through",
			worker:  WorkerFunc(func(context.Context, *Job) error { return nil }),
			perr:    errors.New("boom"),
			wantErr: true,
		},
		{
			name:   "nil error passes through",
			worker: worker_configTestWorker{},
			perr:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := worker_configNewCapStore()
			var store Store = rec
			if tc.bare {
				store = NewInMemoryStore()
			}

			final := func(context.Context, *Job) error { return tc.perr }
			h := WorkerMiddleware(store, clk)(final)

			ctx := context.Background()
			if tc.worker != nil {
				ctx = worker_configWithWorker(ctx, tc.worker)
			}
			job := &Job{ID: jobID, Attempt: 1, MaxAttempts: 3, State: StateExecuting}

			gotErr := h(ctx, job)
			if (gotErr != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", gotErr, tc.wantErr)
			}
			if tc.bare {
				return // nothing recorded on a capability-less store
			}
			if len(rec.snoozes) != tc.wantSnoozes {
				t.Fatalf("snoozes = %d, want %d", len(rec.snoozes), tc.wantSnoozes)
			}
			if len(rec.cancels) != tc.wantCancels {
				t.Fatalf("cancels = %d, want %d", len(rec.cancels), tc.wantCancels)
			}
			if len(rec.retries) != tc.wantRetries {
				t.Fatalf("retries = %d, want %d", len(rec.retries), tc.wantRetries)
			}
			if tc.wantSnoozes == 1 {
				if rec.snoozes[0].id != jobID {
					t.Errorf("snooze id = %d, want %d", rec.snoozes[0].id, jobID)
				}
				if !rec.snoozes[0].until.Equal(tc.wantUntil) {
					t.Errorf("snooze until = %v, want %v", rec.snoozes[0].until, tc.wantUntil)
				}
				if !rec.snoozes[0].now.Equal(baseTime) {
					t.Errorf("snooze now = %v, want %v", rec.snoozes[0].now, baseTime)
				}
			}
			if tc.wantCancels == 1 && rec.cancels[0] != jobID {
				t.Errorf("cancel id = %d, want %d", rec.cancels[0], jobID)
			}
			if tc.wantRetries == 1 {
				if rec.retries[0].id != jobID {
					t.Errorf("retry id = %d, want %d", rec.retries[0].id, jobID)
				}
				if !rec.retries[0].now.Equal(tc.wantRetryAt) {
					t.Errorf("retry at = %v, want %v", rec.retries[0].now, tc.wantRetryAt)
				}
			}
		})
	}
}

func TestWorkerMiddlewareTightensTimeout(t *testing.T) {
	clk := newFakeClock(baseTime)
	store := worker_configNewCapStore()

	tests := []struct {
		name         string
		timeout      time.Duration
		wantDeadline bool
	}{
		{name: "timeout sets a deadline", timeout: 50 * time.Millisecond, wantDeadline: true},
		{name: "zero timeout leaves context untouched", timeout: 0, wantDeadline: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotDeadline bool
			final := func(ctx context.Context, _ *Job) error {
				_, gotDeadline = ctx.Deadline()
				return nil
			}
			w := worker_configTestWorker{opts: WorkerOptions{Timeout: tc.timeout}}
			h := WorkerMiddleware(store, clk)(final)
			ctx := worker_configWithWorker(context.Background(), w)

			if err := h(ctx, &Job{ID: 1, State: StateExecuting}); err != nil {
				t.Fatalf("handler err = %v", err)
			}
			if gotDeadline != tc.wantDeadline {
				t.Errorf("deadline present = %v, want %v", gotDeadline, tc.wantDeadline)
			}
		})
	}
}
