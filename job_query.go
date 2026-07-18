package oban

import "time"

// IsFinished reports whether the job is in a terminal state and will not run
// again: [StateCompleted], [StateDiscarded] or [StateCancelled].
func (j *Job) IsFinished() bool {
	switch j.State {
	case StateCompleted, StateDiscarded, StateCancelled:
		return true
	default:
		return false
	}
}

// InState reports whether the job's current state is any of the given states.
// Calling it with no states returns false.
func (j *Job) InState(states ...State) bool {
	for _, st := range states {
		if j.State == st {
			return true
		}
	}
	return false
}

// AttemptsRemaining returns how many attempts the job has left before it is
// discarded: MaxAttempts minus the current Attempt, floored at zero.
func (j *Job) AttemptsRemaining() int {
	n := j.MaxAttempts - j.Attempt
	if n < 0 {
		return 0
	}
	return n
}

// HasErrored reports whether the job has recorded at least one failed attempt.
func (j *Job) HasErrored() bool {
	return len(j.Errors) > 0
}

// ExecutionDuration returns how long the job's last run took: CompletedAt or
// DiscardedAt minus AttemptedAt. It returns zero when the job has not been
// attempted, has not yet finished, or the timestamps are inconsistent (a
// negative span is clamped to zero).
func (j *Job) ExecutionDuration() time.Duration {
	if j.AttemptedAt.IsZero() {
		return 0
	}
	var end time.Time
	switch {
	case !j.CompletedAt.IsZero():
		end = j.CompletedAt
	case !j.DiscardedAt.IsZero():
		end = j.DiscardedAt
	default:
		return 0
	}
	d := end.Sub(j.AttemptedAt)
	if d < 0 {
		return 0
	}
	return d
}

// Age returns how long ago the job was inserted, measured from now: now minus
// InsertedAt. It returns zero when InsertedAt is unset or in the future.
func (j *Job) Age(now time.Time) time.Duration {
	if j.InsertedAt.IsZero() {
		return 0
	}
	d := now.Sub(j.InsertedAt)
	if d < 0 {
		return 0
	}
	return d
}
