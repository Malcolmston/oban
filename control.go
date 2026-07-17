package oban

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ControlledStore is a [Store] decorator that adds an operator control plane on
// top of any inner store. It lets an operator pause, resume and scale live
// queues without restarting the engine: because the engine fetches work by
// calling the wrapped store's FetchAvailable, changes take effect on the next
// poll.
//
// Every [Store] method other than FetchAvailable is delegated to the inner store
// unchanged, so uniqueness, ordering and all persistence semantics are
// preserved. FetchAvailable additionally consults the pause and scale state:
// a paused queue yields no work, and a scaled queue has its fetch batch capped.
//
// A zero ControlledStore is not usable; construct one with [NewControlledStore].
// ControlledStore is safe for concurrent use provided the inner store is.
type ControlledStore struct {
	inner Store

	mu     sync.RWMutex
	paused map[string]bool
	caps   map[string]int
}

// Compile-time assertion that ControlledStore satisfies the Store contract.
var _ Store = (*ControlledStore)(nil)

// NewControlledStore returns a [ControlledStore] wrapping inner. The returned
// store implements [Store] by delegating every method to inner, adding only the
// pause/resume/scale behavior to FetchAvailable. All queues start unpaused and
// uncapped.
func NewControlledStore(inner Store) *ControlledStore {
	return &ControlledStore{
		inner:  inner,
		paused: make(map[string]bool),
		caps:   make(map[string]int),
	}
}

// Pause marks queue as paused. While paused, FetchAvailable returns no jobs for
// that queue, so the engine stops handing out new work for it; jobs already
// executing are unaffected. Pausing an already-paused queue is a no-op.
func (c *ControlledStore) Pause(queue string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused[queue] = true
}

// Resume clears the paused state for queue, allowing FetchAvailable to hand out
// work for it again. Resuming a queue that is not paused is a no-op.
func (c *ControlledStore) Resume(queue string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.paused, queue)
}

// IsPaused reports whether queue is currently paused.
func (c *ControlledStore) IsPaused(queue string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paused[queue]
}

// Scale sets a dynamic cap on the number of jobs FetchAvailable will fetch for
// queue in a single poll: the effective limit becomes min(limit, maxBatch). A
// non-positive maxBatch removes the cap, restoring the engine's own limit.
func (c *ControlledStore) Scale(queue string, maxBatch int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxBatch <= 0 {
		delete(c.caps, queue)
		return
	}
	c.caps[queue] = maxBatch
}

// FetchAvailable implements [Store]. It returns nil, nil when queue is paused;
// otherwise it delegates to the inner store, capping limit at the queue's scale
// cap (min(limit, cap)) when one is set. Ordering, locking and every other fetch
// guarantee are those of the inner store.
func (c *ControlledStore) FetchAvailable(ctx context.Context, queue string, limit int, now time.Time) ([]*Job, error) {
	c.mu.RLock()
	paused := c.paused[queue]
	batchCap := c.caps[queue]
	c.mu.RUnlock()

	if paused {
		return nil, nil
	}
	if batchCap > 0 && batchCap < limit {
		limit = batchCap
	}
	return c.inner.FetchAvailable(ctx, queue, limit, now)
}

// Enqueue implements [Store] by delegating to the inner store.
func (c *ControlledStore) Enqueue(ctx context.Context, job *Job) (*Job, bool, error) {
	return c.inner.Enqueue(ctx, job)
}

// Complete implements [Store] by delegating to the inner store.
func (c *ControlledStore) Complete(ctx context.Context, id int64, now time.Time) error {
	return c.inner.Complete(ctx, id, now)
}

// Retry implements [Store] by delegating to the inner store.
func (c *ControlledStore) Retry(ctx context.Context, id int64, scheduledAt time.Time, lastErr error, now time.Time) error {
	return c.inner.Retry(ctx, id, scheduledAt, lastErr, now)
}

// Discard implements [Store] by delegating to the inner store.
func (c *ControlledStore) Discard(ctx context.Context, id int64, lastErr error, now time.Time) error {
	return c.inner.Discard(ctx, id, lastErr, now)
}

// Get implements [Store] by delegating to the inner store.
func (c *ControlledStore) Get(ctx context.Context, id int64) (*Job, error) {
	return c.inner.Get(ctx, id)
}

// ErrUnsupported is returned by [Controller] methods when the underlying store
// does not implement the optional capability the operation requires.
var ErrUnsupported = errors.New("oban: store does not support this operation")

// CancelableStore is the optional capability a [Store] implements to support
// cancelling a job. Cancel moves the job with the given id to [StateCancelled].
// It is implemented by [SQLStore].
type CancelableStore interface {
	// Cancel moves the job with the given id to StateCancelled as of now.
	Cancel(ctx context.Context, id int64, now time.Time) error
}

// RetryableStore is the optional capability a [Store] implements to support
// forcing a job to run again immediately. RetryNow moves a retryable or
// discarded job back to [StateAvailable] with scheduled_at set to now, clearing
// the attempt-cap conditions that stopped it. It is implemented by [SQLStore].
type RetryableStore interface {
	// RetryNow makes the job with the given id runnable again as of now.
	RetryNow(ctx context.Context, id int64, now time.Time) error
}

// DeletableStore is the optional capability a [Store] implements to support
// permanently removing a job. It is implemented by [SQLStore].
type DeletableStore interface {
	// Delete removes the job with the given id from the store.
	Delete(ctx context.Context, id int64) error
}

// Controller is an operator tool for acting on individual jobs: cancelling,
// forcing an immediate retry, or deleting them. It works against any [Store]
// that implements the corresponding optional capability
// ([CancelableStore], [RetryableStore], [DeletableStore]) and returns
// [ErrUnsupported] when the store lacks it, so it degrades gracefully.
//
// Construct one with [NewController].
type Controller struct {
	store Store
	clock Clock
}

// NewController returns a [Controller] that acts on store, using clock to stamp
// the transition times of the operations it performs. A nil clock defaults to
// [SystemClock].
func NewController(store Store, clock Clock) *Controller {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Controller{store: store, clock: clock}
}

// Cancel cancels the job with the given id, moving it to [StateCancelled]. It
// returns [ErrUnsupported] if the store cannot cancel jobs, or [ErrJobNotFound]
// if no job with that id exists.
func (m *Controller) Cancel(ctx context.Context, id int64) error {
	cs, ok := m.store.(CancelableStore)
	if !ok {
		return ErrUnsupported
	}
	if err := m.controlEnsureExists(ctx, id); err != nil {
		return err
	}
	return cs.Cancel(ctx, id, m.clock.Now())
}

// Retry forces the job with the given id to run again immediately, moving it to
// [StateAvailable] scheduled at the current time. It returns [ErrUnsupported] if
// the store cannot force a retry, or [ErrJobNotFound] if no job with that id
// exists.
func (m *Controller) Retry(ctx context.Context, id int64) error {
	rs, ok := m.store.(RetryableStore)
	if !ok {
		return ErrUnsupported
	}
	if err := m.controlEnsureExists(ctx, id); err != nil {
		return err
	}
	return rs.RetryNow(ctx, id, m.clock.Now())
}

// Delete permanently removes the job with the given id. It returns
// [ErrUnsupported] if the store cannot delete jobs, or [ErrJobNotFound] if no
// job with that id exists.
func (m *Controller) Delete(ctx context.Context, id int64) error {
	ds, ok := m.store.(DeletableStore)
	if !ok {
		return ErrUnsupported
	}
	if err := m.controlEnsureExists(ctx, id); err != nil {
		return err
	}
	return ds.Delete(ctx, id)
}

// controlEnsureExists returns nil if a job with the given id exists, or the
// error from the store's Get (notably [ErrJobNotFound]). It lets Controller
// report a missing job even when the underlying capability treats an unknown id
// as a silent no-op.
func (m *Controller) controlEnsureExists(ctx context.Context, id int64) error {
	_, err := m.store.Get(ctx, id)
	return err
}
