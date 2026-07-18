package oban

import (
	"context"
	"time"
)

// PerformOutcome classifies the result of running a [Worker] against a [Job]
// with [RunJob] or [PerformJob], distinguishing an ordinary success or failure
// from the {:snooze} and {:cancel} result signals.
type PerformOutcome string

const (
	// OutcomeComplete indicates the worker returned nil.
	OutcomeComplete PerformOutcome = "complete"
	// OutcomeError indicates the worker returned an ordinary error (a retry or
	// discard in the running engine).
	OutcomeError PerformOutcome = "error"
	// OutcomeSnooze indicates the worker returned a [SnoozeError] (see [Snooze]).
	OutcomeSnooze PerformOutcome = "snooze"
	// OutcomeCancel indicates the worker returned a [CancelError] (see
	// [CancelJob]).
	OutcomeCancel PerformOutcome = "cancel"
)

// PerformResult captures the classified outcome of running a worker once,
// without any store transitions. It lets a test assert on a worker's return
// value — including the snooze and cancel signals — the way Elixir Oban's
// perform_job/2 test helper does.
type PerformResult struct {
	// Outcome is the classified result.
	Outcome PerformOutcome
	// Err is the raw error the worker returned, or nil on success. It is set for
	// every non-complete outcome, including snooze and cancel.
	Err error
	// SnoozeFor is the requested snooze duration when Outcome is
	// [OutcomeSnooze], and zero otherwise.
	SnoozeFor time.Duration
	// CancelReason is the cancellation reason when Outcome is [OutcomeCancel],
	// and empty otherwise.
	CancelReason string
}

// RunJob executes w.Perform(ctx, job) once and classifies the result into a
// [PerformResult], recognizing the [Snooze] and [CancelJob] signals via
// [IsSnooze] and [IsCancel]. It performs no store transitions and does not
// mutate job beyond whatever the worker itself does, making it a self-contained
// unit-test helper for a worker's logic.
func RunJob(ctx context.Context, w Worker, job *Job) PerformResult {
	err := w.Perform(ctx, job)
	if err == nil {
		return PerformResult{Outcome: OutcomeComplete}
	}
	if d, ok := IsSnooze(err); ok {
		return PerformResult{Outcome: OutcomeSnooze, Err: err, SnoozeFor: d}
	}
	if reason, ok := IsCancel(err); ok {
		return PerformResult{Outcome: OutcomeCancel, Err: err, CancelReason: reason}
	}
	return PerformResult{Outcome: OutcomeError, Err: err}
}

// PerformJob resolves the worker registered under job.Worker in reg and runs it
// against job with [RunJob]. It returns an error only when no worker is
// registered for the job's Worker name; the worker's own result is reported in
// the [PerformResult]. It is the registry-aware companion to [RunJob].
func PerformJob(ctx context.Context, reg *Registry, job *Job) (PerformResult, error) {
	w, ok := reg.Get(job.Worker)
	if !ok {
		return PerformResult{}, ErrJobNotFound
	}
	return RunJob(ctx, w, job), nil
}

// AssertEnqueued reports whether at least one job in store matches filter. It is
// the affirmative half of the Elixir-Oban-style enqueue assertions and is
// intended for use in tests: a true result means the expected job was enqueued.
func AssertEnqueued(ctx context.Context, store Store, filter JobFilter) (bool, error) {
	n, err := CountJobs(ctx, store, filter)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RefuteEnqueued reports whether no job in store matches filter. It is the
// negative half of the enqueue assertions: a true result means nothing matching
// was enqueued. It is exactly the negation of [AssertEnqueued].
func RefuteEnqueued(ctx context.Context, store Store, filter JobFilter) (bool, error) {
	n, err := CountJobs(ctx, store, filter)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}
