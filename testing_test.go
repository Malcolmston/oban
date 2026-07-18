package oban

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunJobOutcomes(t *testing.T) {
	ctx := context.Background()
	job := &Job{Worker: "w"}

	tests := []struct {
		name        string
		fn          func(context.Context, *Job) error
		wantOutcome PerformOutcome
		check       func(t *testing.T, r PerformResult)
	}{
		{
			name:        "complete",
			fn:          func(context.Context, *Job) error { return nil },
			wantOutcome: OutcomeComplete,
		},
		{
			name:        "error",
			fn:          func(context.Context, *Job) error { return errors.New("boom") },
			wantOutcome: OutcomeError,
		},
		{
			name:        "snooze",
			fn:          func(context.Context, *Job) error { return Snooze(90 * time.Second) },
			wantOutcome: OutcomeSnooze,
			check: func(t *testing.T, r PerformResult) {
				if r.SnoozeFor != 90*time.Second {
					t.Errorf("SnoozeFor = %v, want 90s", r.SnoozeFor)
				}
			},
		},
		{
			name:        "cancel",
			fn:          func(context.Context, *Job) error { return CancelJob("done") },
			wantOutcome: OutcomeCancel,
			check: func(t *testing.T, r PerformResult) {
				if r.CancelReason != "done" {
					t.Errorf("CancelReason = %q, want done", r.CancelReason)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := RunJob(ctx, WorkerFunc(tc.fn), job)
			if r.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q", r.Outcome, tc.wantOutcome)
			}
			if tc.wantOutcome != OutcomeComplete && r.Err == nil {
				t.Error("non-complete outcome should carry Err")
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

func TestPerformJob(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry()
	reg.RegisterFunc("greeter", func(context.Context, *Job) error { return nil })

	r, err := PerformJob(ctx, reg, &Job{Worker: "greeter"})
	if err != nil {
		t.Fatalf("PerformJob: %v", err)
	}
	if r.Outcome != OutcomeComplete {
		t.Errorf("outcome = %q, want complete", r.Outcome)
	}

	// Unknown worker returns an error, not a panic.
	if _, err := PerformJob(ctx, reg, &Job{Worker: "missing"}); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("missing worker: got %v, want ErrJobNotFound", err)
	}
}

func TestAssertRefuteEnqueued(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j, _ := NewJob("email", map[string]string{"to": "a@b.com"}, WithQueue("mailers"))
	j.InsertedAt = baseTime
	_, _, _ = s.Enqueue(ctx, j)

	ok, err := AssertEnqueued(ctx, s, JobFilter{Worker: "email", Queue: "mailers"})
	if err != nil {
		t.Fatalf("AssertEnqueued: %v", err)
	}
	if !ok {
		t.Error("expected email job to be asserted enqueued")
	}

	refuted, err := RefuteEnqueued(ctx, s, JobFilter{Worker: "sms"})
	if err != nil {
		t.Fatalf("RefuteEnqueued: %v", err)
	}
	if !refuted {
		t.Error("expected sms job to be refuted")
	}

	// Assert and refute are exact negations for the same filter.
	f := JobFilter{Worker: "email"}
	a, _ := AssertEnqueued(ctx, s, f)
	r, _ := RefuteEnqueued(ctx, s, f)
	if a == r {
		t.Errorf("assert (%v) and refute (%v) must differ", a, r)
	}
}
