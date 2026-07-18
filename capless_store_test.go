package oban

import (
	"context"
	"time"
)

// caplessStore exposes only the core [Store] interface of an inner
// [InMemoryStore], deliberately hiding the optional capabilities (cancel, retry,
// delete, snooze, prune, rescue, unique, tags) so that tests can exercise the
// capability-absent code paths. Since InMemoryStore itself now implements all of
// those capabilities, tests that want a "bare" store use this wrapper instead.
type caplessStore struct{ inner *InMemoryStore }

func newCaplessStore() *caplessStore { return &caplessStore{inner: NewInMemoryStore()} }

func (b *caplessStore) Enqueue(ctx context.Context, job *Job) (*Job, bool, error) {
	return b.inner.Enqueue(ctx, job)
}

func (b *caplessStore) FetchAvailable(ctx context.Context, queue string, limit int, now time.Time) ([]*Job, error) {
	return b.inner.FetchAvailable(ctx, queue, limit, now)
}

func (b *caplessStore) Complete(ctx context.Context, id int64, now time.Time) error {
	return b.inner.Complete(ctx, id, now)
}

func (b *caplessStore) Retry(ctx context.Context, id int64, scheduledAt time.Time, lastErr error, now time.Time) error {
	return b.inner.Retry(ctx, id, scheduledAt, lastErr, now)
}

func (b *caplessStore) Discard(ctx context.Context, id int64, lastErr error, now time.Time) error {
	return b.inner.Discard(ctx, id, lastErr, now)
}

func (b *caplessStore) Get(ctx context.Context, id int64) (*Job, error) {
	return b.inner.Get(ctx, id)
}
