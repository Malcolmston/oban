package oban

import (
	"context"
	"time"
)

// Handler runs a job attempt. It is the unit that [Middleware] wraps; the
// innermost handler invokes the registered [Worker].
type Handler func(ctx context.Context, job *Job) error

// Middleware wraps a [Handler] to observe or alter job execution. Middleware is
// applied so that the first element of the configured slice is the outermost
// wrapper:
//
//	mw[0]( mw[1]( ... worker.Perform ) )
//
// so mw[0] sees the attempt begin first and its result last. Middleware should
// call next(ctx, job) to run the wrapped handler and may inspect the returned
// error, but must not retain job after returning.
type Middleware func(next Handler) Handler

// chain composes middleware around a final handler. The first middleware in the
// slice becomes the outermost wrapper.
func chain(final Handler, mws []Middleware) Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Telemetry receives lifecycle callbacks for every job attempt. Any nil field
// is skipped, so implementations may set only the hooks they need. All
// callbacks run synchronously on the worker goroutine and must not block for
// long.
type Telemetry struct {
	// OnStart is called just before the worker runs, with the attempt's context.
	OnStart func(ctx context.Context, job *Job)
	// OnComplete is called after a successful attempt with its wall-clock
	// duration.
	OnComplete func(ctx context.Context, job *Job, dur time.Duration)
	// OnError is called after a failed attempt with the error and duration.
	OnError func(ctx context.Context, job *Job, err error, dur time.Duration)
}

// Middleware adapts the telemetry hooks into a [Middleware] value using clock to
// measure duration.
func (t Telemetry) Middleware(clock Clock) Middleware {
	if clock == nil {
		clock = SystemClock{}
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, job *Job) error {
			if t.OnStart != nil {
				t.OnStart(ctx, job)
			}
			start := clock.Now()
			err := next(ctx, job)
			dur := clock.Now().Sub(start)
			if err != nil {
				if t.OnError != nil {
					t.OnError(ctx, job, err, dur)
				}
			} else if t.OnComplete != nil {
				t.OnComplete(ctx, job, dur)
			}
			return err
		}
	}
}
