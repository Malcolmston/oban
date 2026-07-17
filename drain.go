package oban

import (
	"context"
	"fmt"
	"time"
)

// drainFetchLimit is the batch size Drain requests from the store on each
// FetchAvailable call. It is deliberately large so that a single fetch returns
// every runnable job in the queue, letting the executor run the queue to
// completion without polling.
const drainFetchLimit = 1 << 30

// drainFarFuture is the instant Drain treats as "now" when
// DrainOptions.WithScheduled is set, so that jobs whose ScheduledAt lies in the
// future are still considered runnable. It is far enough ahead to dominate any
// realistic schedule.
var drainFarFuture = time.Unix(1<<62, 0)

// DrainResult tallies the outcomes of a Drain run. Each job that is processed
// increments exactly one bucket, so [DrainResult.Total] equals the number of
// jobs the drain ran.
type DrainResult struct {
	// Completed counts jobs whose worker returned a nil error.
	Completed int
	// Retried counts failed jobs that still had attempts remaining and were
	// rescheduled with a backoff delay.
	Retried int
	// Discarded counts failed jobs that exhausted their attempts and were moved
	// to the discarded state.
	Discarded int
	// Cancelled counts jobs whose worker returned the [CancelError] sentinel and
	// whose store implements the cancel capability.
	Cancelled int
	// Snoozed counts jobs whose worker returned the [SnoozeError] sentinel and
	// whose store implements the snooze capability.
	Snoozed int
	// Failed counts jobs that could not be executed because no worker was
	// registered for them. Such jobs are discarded so the drain can make
	// progress instead of fetching them forever.
	Failed int
}

// Total returns the total number of jobs processed across every outcome bucket.
func (r DrainResult) Total() int {
	return r.Completed + r.Retried + r.Discarded + r.Cancelled + r.Snoozed + r.Failed
}

// DrainOptions configures a [Drain] run.
type DrainOptions struct {
	// Queue names the queue to drain. It is required; Drain returns an error if
	// it is empty.
	Queue string

	// WithRecursion keeps draining jobs that become available during the run
	// (for example a job that fails, is retried and then becomes ready again)
	// until a fetch returns no jobs and the queue is empty. When false, Drain
	// makes a single pass over the jobs that are runnable at the start.
	WithRecursion bool

	// WithScheduled also runs jobs whose ScheduledAt is in the future by
	// treating the current time as +inf for the duration of this drain, so that
	// scheduled and retryable jobs are fetched regardless of their schedule.
	WithScheduled bool

	// Backoff is the retry-delay policy applied to failed jobs that still have
	// attempts remaining. It defaults to &ExponentialBackoff{}.
	Backoff Backoff

	// Timeout bounds each attempt's context. It defaults to [DefaultJobTimeout].
	Timeout time.Duration

	// Middleware wraps every attempt in the same chain order the engine uses:
	// the first element is the outermost wrapper (see [Middleware]).
	Middleware []Middleware
}

// drainCanceler is the optional capability a [Store] implements to support the
// [CancelError] sentinel: moving a job to the cancelled state. The signature
// matches the cancel method the engine's control area relies on.
type drainCanceler interface {
	Cancel(ctx context.Context, id int64, now time.Time) error
}

// drainSnoozer is the optional capability a [Store] implements to support the
// [SnoozeError] sentinel: rescheduling a job to run again at scheduledAt without
// recording the attempt as a failure.
type drainSnoozer interface {
	Snooze(ctx context.Context, id int64, scheduledAt time.Time, now time.Time) error
}

// Drain synchronously runs the runnable jobs in opts.Queue to completion,
// mirroring the engine's execute logic (fetch, run the worker, then
// Complete/Retry/Discard) without goroutines, polling or sleeps. It is intended
// for tests that need jobs to run deterministically without calling
// [Oban.Start]/[Oban.Stop].
//
// It repeatedly calls store.FetchAvailable for the queue with a large limit and,
// for each returned job, resolves the [Worker] from reg, wraps opts.Middleware
// around the worker invocation using the same chain order the engine uses, runs
// Perform under a context.WithTimeout, then applies the result exactly as the
// engine does: a nil error completes the job; a non-nil error retries it with
// opts.Backoff when attempts remain and discards it once they are exhausted. A
// worker that returns the [SnoozeError] or [CancelError] sentinel is snoozed or
// cancelled when the store supports that capability, otherwise the sentinel is
// treated as an ordinary error. A job whose worker is not registered is
// discarded and counted as Failed.
//
// clock supplies the current time and defaults to [SystemClock] when nil. store
// and reg must be non-nil. Drain returns the aggregate [DrainResult] and the
// first fetch error, if any.
func Drain(ctx context.Context, store Store, reg *Registry, clock Clock, opts DrainOptions) (DrainResult, error) {
	var result DrainResult
	if store == nil {
		return result, fmt.Errorf("oban: drain: nil store")
	}
	if reg == nil {
		return result, fmt.Errorf("oban: drain: nil registry")
	}
	if opts.Queue == "" {
		return result, fmt.Errorf("oban: drain: queue is required")
	}
	if clock == nil {
		clock = SystemClock{}
	}
	backoff := opts.Backoff
	if backoff == nil {
		backoff = &ExponentialBackoff{}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultJobTimeout
	}

	// final is the innermost handler: it invokes the registered worker and
	// converts panics into errors, matching the engine's runWorker.
	final := func(ctx context.Context, job *Job) (err error) {
		w, ok := reg.Get(job.Worker)
		if !ok {
			return fmt.Errorf("oban: no worker registered for %q", job.Worker)
		}
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("oban: worker %q panicked: %v", job.Worker, r)
			}
		}()
		return w.Perform(ctx, job)
	}
	handler := chain(final, opts.Middleware)

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		fetchNow := clock.Now()
		if opts.WithScheduled {
			fetchNow = drainFarFuture
		}
		jobs, err := store.FetchAvailable(ctx, opts.Queue, drainFetchLimit, fetchNow)
		if err != nil {
			return result, err
		}
		if len(jobs) == 0 {
			return result, nil
		}
		for _, job := range jobs {
			drainRun(ctx, store, reg, handler, backoff, timeout, clock, job, &result)
		}
		if !opts.WithRecursion {
			return result, nil
		}
	}
}

// DrainQueue is a convenience wrapper around [Drain] that reads the store,
// registry, clock, backoff and job timeout from o and drains the named queue
// with otherwise default options.
func DrainQueue(ctx context.Context, o *Oban, queue string) (DrainResult, error) {
	if o == nil {
		return DrainResult{}, fmt.Errorf("oban: drain queue: nil engine")
	}
	return Drain(ctx, o.Store(), o.Registry(), o.clock, DrainOptions{
		Queue:   queue,
		Backoff: o.backoff,
		Timeout: o.jobTimeout,
	})
}

// drainRun executes a single attempt of job and applies its result to the store,
// incrementing the matching bucket in result.
func drainRun(ctx context.Context, store Store, reg *Registry, handler Handler, backoff Backoff, timeout time.Duration, clock Clock, job *Job, result *DrainResult) {
	// A job with no registered worker cannot run; discard it so the drain makes
	// progress and count it as Failed.
	if _, ok := reg.Get(job.Worker); !ok {
		_ = store.Discard(ctx, job.ID, fmt.Errorf("oban: no worker registered for %q", job.Worker), clock.Now())
		result.Failed++
		return
	}

	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := handler(attemptCtx, job)
	now := clock.Now()

	if err == nil {
		_ = store.Complete(ctx, job.ID, now)
		result.Completed++
		return
	}
	if _, ok := IsCancel(err); ok {
		if c, ok := store.(drainCanceler); ok {
			_ = c.Cancel(ctx, job.ID, now)
			result.Cancelled++
			return
		}
		drainFail(ctx, store, backoff, job, err, now, result)
		return
	}
	if d, ok := IsSnooze(err); ok {
		if s, ok := store.(drainSnoozer); ok {
			until := now.Add(d)
			if d <= 0 {
				until = now.Add(backoff.Next(job.Attempt))
			}
			_ = s.Snooze(ctx, job.ID, until, now)
			result.Snoozed++
			return
		}
		drainFail(ctx, store, backoff, job, err, now, result)
		return
	}
	drainFail(ctx, store, backoff, job, err, now, result)
}

// drainFail applies the ordinary failure path: discard when attempts are
// exhausted, otherwise retry with a backoff delay.
func drainFail(ctx context.Context, store Store, backoff Backoff, job *Job, err error, now time.Time, result *DrainResult) {
	if job.Attempt >= job.MaxAttempts {
		_ = store.Discard(ctx, job.ID, err, now)
		result.Discarded++
		return
	}
	_ = store.Retry(ctx, job.ID, now.Add(backoff.Next(job.Attempt)), err, now)
	result.Retried++
}
