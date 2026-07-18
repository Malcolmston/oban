package oban

import (
	"errors"
	"testing"
	"time"
)

func TestJobIsFinishedAndInState(t *testing.T) {
	tests := []struct {
		state    State
		finished bool
	}{
		{StateScheduled, false},
		{StateAvailable, false},
		{StateExecuting, false},
		{StateRetryable, false},
		{StateCompleted, true},
		{StateDiscarded, true},
		{StateCancelled, true},
	}
	for _, tc := range tests {
		j := &Job{State: tc.state}
		if got := j.IsFinished(); got != tc.finished {
			t.Errorf("%s IsFinished = %v, want %v", tc.state, got, tc.finished)
		}
	}

	j := &Job{State: StateRetryable}
	if !j.InState(StateAvailable, StateRetryable) {
		t.Error("InState should match retryable")
	}
	if j.InState(StateCompleted) {
		t.Error("InState should not match completed")
	}
	if j.InState() {
		t.Error("InState() with no args should be false")
	}
}

func TestJobAttemptsRemaining(t *testing.T) {
	tests := []struct {
		attempt, max, want int
	}{
		{0, 20, 20},
		{1, 20, 19},
		{20, 20, 0},
		{21, 20, 0}, // never negative
	}
	for _, tc := range tests {
		j := &Job{Attempt: tc.attempt, MaxAttempts: tc.max}
		if got := j.AttemptsRemaining(); got != tc.want {
			t.Errorf("attempt=%d max=%d: got %d, want %d", tc.attempt, tc.max, got, tc.want)
		}
	}
}

func TestJobHasErrored(t *testing.T) {
	j := &Job{}
	if j.HasErrored() {
		t.Error("fresh job should not have errored")
	}
	j.recordError(errors.New("boom"), baseTime)
	if !j.HasErrored() {
		t.Error("job with recorded error should report HasErrored")
	}
}

func TestJobExecutionDuration(t *testing.T) {
	// Completed job.
	j := &Job{
		State:       StateCompleted,
		AttemptedAt: baseTime,
		CompletedAt: baseTime.Add(3 * time.Second),
	}
	if got := j.ExecutionDuration(); got != 3*time.Second {
		t.Errorf("completed duration = %v, want 3s", got)
	}
	// Discarded job.
	d := &Job{
		State:       StateDiscarded,
		AttemptedAt: baseTime,
		DiscardedAt: baseTime.Add(2 * time.Second),
	}
	if got := d.ExecutionDuration(); got != 2*time.Second {
		t.Errorf("discarded duration = %v, want 2s", got)
	}
	// Never attempted.
	if got := (&Job{}).ExecutionDuration(); got != 0 {
		t.Errorf("unattempted duration = %v, want 0", got)
	}
	// Attempted but not finished.
	running := &Job{State: StateExecuting, AttemptedAt: baseTime}
	if got := running.ExecutionDuration(); got != 0 {
		t.Errorf("running duration = %v, want 0", got)
	}
}

func TestJobAge(t *testing.T) {
	j := &Job{InsertedAt: baseTime}
	if got := j.Age(baseTime.Add(time.Hour)); got != time.Hour {
		t.Errorf("age = %v, want 1h", got)
	}
	// Future insertion (clock skew) clamps to zero.
	if got := j.Age(baseTime.Add(-time.Hour)); got != 0 {
		t.Errorf("age = %v, want 0", got)
	}
	// Zero insertion time yields zero.
	if got := (&Job{}).Age(baseTime); got != 0 {
		t.Errorf("age = %v, want 0", got)
	}
}
