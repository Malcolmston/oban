package oban

import (
	"context"
	"errors"
	"time"
)

// WorkerOptions carries Elixir-Oban-style per-worker configuration. A [Worker]
// exposes it by implementing [ConfiguredWorker]; the values then act as
// defaults for jobs built with [BuildJob] and as execution policy for
// [WorkerMiddleware].
//
// The zero value requests no per-worker configuration: an empty Queue, a
// non-positive MaxAttempts, Priority and Timeout, and nil Tags, Unique and
// Backoff are all treated as "unset" and fall back to the engine's own
// defaults.
type WorkerOptions struct {
	// Queue is the queue the worker's jobs default to. Empty means the job's
	// own queue (ultimately [DefaultQueue]) is kept.
	Queue string
	// MaxAttempts is the default attempt cap for the worker's jobs. A
	// non-positive value leaves the job's own cap in place.
	MaxAttempts int
	// Priority is the default priority (lower runs first) for the worker's
	// jobs. Zero, the package default, is treated as unset.
	Priority int
	// Timeout bounds a single attempt. When positive, [WorkerMiddleware]
	// tightens the engine's per-attempt context to this duration; zero leaves
	// the engine timeout untouched.
	Timeout time.Duration
	// Tags are free-form labels associated with the worker's jobs. They are
	// carried for insert-time use (see [InsertOpts]) and are not applied to the
	// [Job] itself by [BuildJob].
	Tags []string
	// Unique, when non-nil, is the default uniqueness rule for the worker's
	// jobs. Like Tags it is consumed at insert time via [InsertOpts].
	Unique *UniqueBy
	// Backoff, when non-nil, is the per-worker retry policy. On an ordinary
	// failure [WorkerMiddleware] reschedules the job with this backoff instead
	// of leaving it to the engine's global policy.
	Backoff Backoff
}

// ConfiguredWorker is the optional interface a [Worker] implements to advertise
// its own [WorkerOptions]. It is purely additive: any Worker may implement it,
// and code that does not care about per-worker configuration can ignore it.
type ConfiguredWorker interface {
	// Config returns the per-worker configuration for this worker.
	Config() WorkerOptions
}

// BuildJob builds a [Job] for the worker registered under name, using w only to
// read its [WorkerOptions] when w implements [ConfiguredWorker].
//
// It starts from [NewJob] and then applies the worker's configured Queue,
// MaxAttempts and Priority as defaults, layering the caller's opts on top so
// that any option the caller passes overrides the worker default. args is
// marshaled to JSON exactly as [NewJob] does.
func BuildJob(name string, w Worker, args any, opts ...JobOption) (*Job, error) {
	var base []JobOption
	if cw, ok := w.(ConfiguredWorker); ok {
		cfg := cw.Config()
		if cfg.Queue != "" {
			base = append(base, WithQueue(cfg.Queue))
		}
		if cfg.MaxAttempts > 0 {
			base = append(base, WithMaxAttempts(cfg.MaxAttempts))
		}
		if cfg.Priority != 0 {
			base = append(base, WithPriority(cfg.Priority))
		}
	}
	// Worker defaults first, caller options last so the caller always wins.
	return NewJob(name, args, append(base, opts...)...)
}

// SnoozeError is the result signal a [Worker.Perform] returns to ask the engine
// to reschedule the job for a later time without counting the attempt as a
// failure. It mirrors Elixir Oban's {:snooze, seconds} return value.
type SnoozeError struct {
	// For is how far into the future the job should be rescheduled, measured
	// from the time the signal is observed.
	For time.Duration
}

// Error implements the error interface.
func (e SnoozeError) Error() string {
	return "oban: snooze for " + e.For.String()
}

// Snooze returns a [SnoozeError] that asks the engine to reschedule the current
// job d into the future without consuming an attempt.
func Snooze(d time.Duration) error {
	return SnoozeError{For: d}
}

// CancelError is the result signal a [Worker.Perform] returns to ask the engine
// to move the job to [StateCancelled] instead of retrying it. It mirrors Elixir
// Oban's {:cancel, reason} return value.
type CancelError struct {
	// Reason is an optional human-readable explanation for the cancellation.
	Reason string
}

// Error implements the error interface.
func (e CancelError) Error() string {
	if e.Reason == "" {
		return "oban: job cancelled"
	}
	return "oban: job cancelled: " + e.Reason
}

// CancelJob returns a [CancelError] that asks the engine to cancel the current
// job, recording reason.
func CancelJob(reason string) error {
	return CancelError{Reason: reason}
}

// IsSnooze reports whether err is (or wraps) a [SnoozeError], returning its
// duration when so. It uses [errors.As], so a wrapped signal is still
// recognized.
func IsSnooze(err error) (time.Duration, bool) {
	var se SnoozeError
	if errors.As(err, &se) {
		return se.For, true
	}
	return 0, false
}

// IsCancel reports whether err is (or wraps) a [CancelError], returning its
// reason when so. It uses [errors.As], so a wrapped signal is still recognized.
func IsCancel(err error) (string, bool) {
	var ce CancelError
	if errors.As(err, &ce) {
		return ce.Reason, true
	}
	return "", false
}

// SnoozableStore is the optional capability a [Store] implements to support the
// {:snooze} result signal. Snooze moves the job with the given id to
// [StateScheduled] with scheduled_at set to until and decrements its attempt so
// that a snooze does not consume one of the job's attempts. Like the other
// finishing transitions it is guarded on the executing state, so a job that has
// already moved on is left untouched.
type SnoozableStore interface {
	// Snooze reschedules the job with the given id to until, treating the
	// current attempt as not spent. now is the observation time.
	Snooze(ctx context.Context, id int64, until time.Time, now time.Time) error
}

// worker_configCtxKey is the context key under which the currently executing
// [Worker] is carried so that [WorkerMiddleware] can recover its
// [WorkerOptions].
type worker_configCtxKey struct{}

// worker_configWithWorker returns a copy of ctx carrying w. The engine wiring
// attaches the resolved worker to the attempt context with it so that
// [WorkerMiddleware] can consult the worker's [WorkerOptions].
func worker_configWithWorker(ctx context.Context, w Worker) context.Context {
	return context.WithValue(ctx, worker_configCtxKey{}, w)
}

// worker_configWorkerFromContext returns the [Worker] carried by ctx, if any.
func worker_configWorkerFromContext(ctx context.Context) (Worker, bool) {
	w, ok := ctx.Value(worker_configCtxKey{}).(Worker)
	return w, ok
}

// WorkerMiddleware returns a [Middleware] that adds per-worker configuration and
// the {:snooze}/{:cancel} result signals on top of the engine's default
// execution. It slots into [Config.Middleware] and cooperates with the store's
// executing-state guard: every terminal action it takes moves the job out of
// [StateExecuting] and returns a nil error, so the engine's subsequent guarded
// Complete becomes a no-op.
//
// Before invoking the next handler, when the running worker implements
// [ConfiguredWorker] and sets a positive Timeout, the middleware tightens the
// attempt context with a [context.WithTimeout] of that duration (never loosening
// the engine's own deadline).
//
// After the handler returns it inspects the error:
//
//   - A [SnoozeError] (see [Snooze]) reschedules the job through
//     [SnoozableStore] at now+For and returns nil, so the snooze does not
//     consume an attempt.
//   - A [CancelError] (see [CancelJob]) cancels the job through
//     [CancelableStore] and returns nil.
//   - Any other error, when the worker configures a per-worker Backoff,
//     reschedules the job through [RetryableStore] at now+Backoff.Next and
//     returns nil; without a per-worker Backoff the error is passed through
//     unchanged for the engine's default retry.
//
// A nil error is passed through unchanged. When a store lacks the capability an
// action requires, or the store call fails, the original error is returned so
// the engine can still apply its default handling. clock supplies the current
// time; a nil clock defaults to [SystemClock].
func WorkerMiddleware(store Store, clock Clock) Middleware {
	if clock == nil {
		clock = SystemClock{}
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, job *Job) error {
			// Recover the worker's configuration, if it advertises any.
			var opts *WorkerOptions
			if w, ok := worker_configWorkerFromContext(ctx); ok {
				if cw, ok := w.(ConfiguredWorker); ok {
					cfg := cw.Config()
					opts = &cfg
				}
			}

			// Tighten the attempt deadline to the worker's Timeout, if set.
			callCtx := ctx
			if opts != nil && opts.Timeout > 0 {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
				defer cancel()
			}

			err := next(callCtx, job)
			if err == nil {
				return nil
			}

			now := clock.Now()
			// Store transitions must outlive the (possibly expired) attempt
			// deadline, mirroring how the engine uses its base job context.
			writeCtx := context.WithoutCancel(ctx)

			if d, ok := IsSnooze(err); ok {
				if ss, ok := store.(SnoozableStore); ok {
					if serr := ss.Snooze(writeCtx, job.ID, now.Add(d), now); serr == nil {
						return nil
					}
				}
				return err
			}

			if _, ok := IsCancel(err); ok {
				if cs, ok := store.(CancelableStore); ok {
					if cerr := cs.Cancel(writeCtx, job.ID, now); cerr == nil {
						return nil
					}
				}
				return err
			}

			// Ordinary failure: apply the per-worker backoff when configured.
			if opts != nil && opts.Backoff != nil {
				if rs, ok := store.(RetryableStore); ok {
					delay := opts.Backoff.Next(job.Attempt)
					if rerr := rs.RetryNow(writeCtx, job.ID, now.Add(delay)); rerr == nil {
						return nil
					}
				}
			}
			return err
		}
	}
}
