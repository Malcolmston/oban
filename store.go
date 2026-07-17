package oban

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrJobNotFound is returned by [Store] methods when no job matches the given
// ID.
var ErrJobNotFound = errors.New("oban: job not found")

// Store persists jobs and mediates the transitions the engine drives. A Store
// must be safe for concurrent use.
//
// The engine owns the clock: every method that depends on the current time
// receives it explicitly (as now), which keeps stores time-agnostic and tests
// deterministic.
//
// # Implementing a persistent store
//
// To back Oban with a database, implement this interface against your storage.
// The contract each method must uphold:
//
//   - Enqueue must be atomic with respect to uniqueness: when job.UniqueKey is
//     set, it must not insert a duplicate if an unfinished job with the same
//     Queue, Worker and UniqueKey exists within UniquePeriod. Return that
//     existing job with inserted=false. Otherwise assign a unique ID, resolve
//     any relative schedule, and insert with inserted=true.
//   - FetchAvailable must atomically select and lock up to limit jobs for the
//     queue whose state is fetchable (available, scheduled or retryable) and
//     whose ScheduledAt <= now, ordered by Priority asc, ScheduledAt asc, ID
//     asc. It must transition each to StateExecuting, increment Attempt, set
//     AttemptedAt, and never hand the same job to two callers (use SELECT ...
//     FOR UPDATE SKIP LOCKED or an equivalent).
//   - Complete, Retry and Discard transition a single job by ID and must be
//     idempotent enough to tolerate a job that has already moved on.
//
// Returned jobs should be copies (or otherwise immutable) so callers cannot
// mutate stored state by reference.
type Store interface {
	// Enqueue inserts job, honoring unique-job de-duplication. It returns the
	// stored job and whether a new row was inserted (false means a matching
	// unfinished job already existed and is returned instead).
	Enqueue(ctx context.Context, job *Job) (result *Job, inserted bool, err error)

	// FetchAvailable atomically locks up to limit runnable jobs for queue,
	// transitions them to StateExecuting and returns them in execution order.
	FetchAvailable(ctx context.Context, queue string, limit int, now time.Time) ([]*Job, error)

	// Complete marks the job completed.
	Complete(ctx context.Context, id int64, now time.Time) error

	// Retry reschedules the job to run at scheduledAt, recording lastErr as a
	// failed attempt and moving it to StateRetryable.
	Retry(ctx context.Context, id int64, scheduledAt time.Time, lastErr error, now time.Time) error

	// Discard marks the job discarded, recording lastErr as a failed attempt.
	Discard(ctx context.Context, id int64, lastErr error, now time.Time) error

	// Get returns a copy of the job with the given ID, or ErrJobNotFound.
	Get(ctx context.Context, id int64) (*Job, error)
}

// InMemoryStore is a complete, concurrency-safe [Store] kept entirely in memory.
// It is intended for tests, development and small single-process deployments;
// all state is lost when the process exits. For durable, multi-process use,
// implement [Store] against a database (see the Store documentation).
type InMemoryStore struct {
	mu     sync.Mutex
	jobs   map[int64]*Job
	nextID int64
}

// NewInMemoryStore returns an empty [InMemoryStore].
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{jobs: make(map[int64]*Job)}
}

// Enqueue implements [Store].
func (s *InMemoryStore) Enqueue(_ context.Context, job *Job) (*Job, bool, error) {
	if job == nil {
		return nil, false, errors.New("oban: cannot enqueue nil job")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := job.InsertedAt
	if now.IsZero() {
		now = time.Now()
	}

	// Unique-job de-duplication: skip insert if a matching unfinished job was
	// inserted within the unique period.
	if job.UniqueKey != "" && job.UniquePeriod > 0 {
		if existing := s.findUnique(job, now); existing != nil {
			return existing.Clone(), false, nil
		}
	}

	stored := job.Clone()
	s.nextID++
	stored.ID = s.nextID
	stored.InsertedAt = now

	// Resolve a relative schedule (WithScheduleIn) against the insert time.
	if job.hasScheduleIn {
		stored.ScheduledAt = now.Add(job.scheduleIn)
	}
	if stored.ScheduledAt.IsZero() {
		stored.ScheduledAt = now
	}
	// Normalize state from the schedule.
	if stored.ScheduledAt.After(now) {
		stored.State = StateScheduled
	} else {
		stored.State = StateAvailable
	}

	s.jobs[stored.ID] = stored
	return stored.Clone(), true, nil
}

// findUnique returns an existing unfinished job that conflicts with job, or nil.
// Caller must hold the mutex.
func (s *InMemoryStore) findUnique(job *Job, now time.Time) *Job {
	cutoff := now.Add(-job.UniquePeriod)
	for _, existing := range s.jobs {
		if existing.Queue != job.Queue || existing.Worker != job.Worker {
			continue
		}
		if existing.UniqueKey != job.UniqueKey {
			continue
		}
		if !unfinishedStates[existing.State] {
			continue
		}
		if existing.InsertedAt.Before(cutoff) {
			continue
		}
		return existing
	}
	return nil
}

// FetchAvailable implements [Store].
func (s *InMemoryStore) FetchAvailable(_ context.Context, queue string, limit int, now time.Time) ([]*Job, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidates []*Job
	for _, j := range s.jobs {
		if j.Queue != queue {
			continue
		}
		if !fetchableStates[j.State] {
			continue
		}
		if j.ScheduledAt.After(now) {
			continue
		}
		candidates = append(candidates, j)
	}

	sort.Slice(candidates, func(a, b int) bool {
		ja, jb := candidates[a], candidates[b]
		if ja.Priority != jb.Priority {
			return ja.Priority < jb.Priority
		}
		if !ja.ScheduledAt.Equal(jb.ScheduledAt) {
			return ja.ScheduledAt.Before(jb.ScheduledAt)
		}
		return ja.ID < jb.ID
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	out := make([]*Job, 0, len(candidates))
	for _, j := range candidates {
		j.State = StateExecuting
		j.Attempt++
		j.AttemptedAt = now
		out = append(out, j.Clone())
	}
	return out, nil
}

// Complete implements [Store].
func (s *InMemoryStore) Complete(_ context.Context, id int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	j.State = StateCompleted
	j.CompletedAt = now
	return nil
}

// Retry implements [Store].
func (s *InMemoryStore) Retry(_ context.Context, id int64, scheduledAt time.Time, lastErr error, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if lastErr != nil {
		j.recordError(lastErr, now)
	}
	j.State = StateRetryable
	j.ScheduledAt = scheduledAt
	return nil
}

// Discard implements [Store].
func (s *InMemoryStore) Discard(_ context.Context, id int64, lastErr error, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if lastErr != nil {
		j.recordError(lastErr, now)
	}
	j.State = StateDiscarded
	j.DiscardedAt = now
	return nil
}

// Get implements [Store].
func (s *InMemoryStore) Get(_ context.Context, id int64) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	return j.Clone(), nil
}

// All returns a snapshot copy of every stored job, ordered by ID. It is a
// convenience for tests and introspection and is not part of the [Store]
// interface.
func (s *InMemoryStore) All() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.Clone())
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// CountByState returns the number of stored jobs in the given state.
func (s *InMemoryStore) CountByState(state State) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.State == state {
			n++
		}
	}
	return n
}
