package oban

import (
	"context"
	"sort"
	"time"
)

// The [InMemoryStore] satisfies every optional store capability the engine,
// control plane and plugins consult. These compile-time assertions keep the
// implementations honest against the interface signatures.
var (
	_ CancelableStore = (*InMemoryStore)(nil)
	_ RetryableStore  = (*InMemoryStore)(nil)
	_ DeletableStore  = (*InMemoryStore)(nil)
	_ SnoozableStore  = (*InMemoryStore)(nil)
	_ PrunableStore   = (*InMemoryStore)(nil)
	_ RescuableStore  = (*InMemoryStore)(nil)
	_ UniqueStore     = (*InMemoryStore)(nil)
	_ TaggableStore   = (*InMemoryStore)(nil)
)

// inmemFinishedStates are the terminal states a job can rest in: it will not run
// again from any of them.
var inmemFinishedStates = map[State]bool{
	StateCompleted: true,
	StateDiscarded: true,
	StateCancelled: true,
}

// Cancel moves the job with the given id to [StateCancelled] unless it is
// already in a terminal state (completed, discarded or cancelled), in which case
// it is a no-op. Cancelling an executing job is permitted: the executing guard
// on Complete, Retry and Discard then prevents the finishing runner from
// overwriting the cancellation. A missing id returns [ErrJobNotFound]. It
// implements [CancelableStore].
func (s *InMemoryStore) Cancel(_ context.Context, id int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if inmemFinishedStates[j.State] {
		return nil
	}
	j.State = StateCancelled
	j.DiscardedAt = now
	return nil
}

// Delete removes the job with the given id and any tags or meta stored for it.
// Deleting a job that does not exist is a no-op. It implements [DeletableStore].
func (s *InMemoryStore) Delete(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	delete(s.tags, id)
	delete(s.meta, id)
	return nil
}

// RetryNow makes the job with the given id runnable again immediately: it moves
// the job to [StateAvailable], schedules it at now and clears its completed and
// discarded timestamps. It is intended for retrying a discarded or cancelled job
// on demand. A missing id returns [ErrJobNotFound]. It implements
// [RetryableStore].
func (s *InMemoryStore) RetryNow(_ context.Context, id int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	j.State = StateAvailable
	j.ScheduledAt = now
	j.CompletedAt = time.Time{}
	j.DiscardedAt = time.Time{}
	return nil
}

// Snooze reschedules the executing job with the given id to run no earlier than
// until, moving it to [StateScheduled] and decrementing its Attempt so the
// snooze does not consume one of the job's attempts. It is guarded on the
// executing state: a job that has already moved on is left untouched (returning
// nil). A missing id returns [ErrJobNotFound]. It implements [SnoozableStore].
func (s *InMemoryStore) Snooze(_ context.Context, id int64, until time.Time, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if j.State != StateExecuting {
		return nil
	}
	j.State = StateScheduled
	j.ScheduledAt = until
	if j.Attempt > 0 {
		j.Attempt--
	}
	return nil
}

// DeleteFinishedBefore removes up to limit jobs whose State is one of states and
// whose finishing time is strictly before cutoff, returning the number removed.
// The finishing time is CompletedAt for completed jobs and DiscardedAt for
// discarded or cancelled jobs, falling back to InsertedAt when unset. Jobs are
// considered in ascending ID order so the limit is deterministic. A non-positive
// limit means no bound. It implements [PrunableStore].
func (s *InMemoryStore) DeleteFinishedBefore(_ context.Context, states []State, cutoff time.Time, limit int) (int64, error) {
	want := make(map[State]bool, len(states))
	for _, st := range states {
		want[st] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]int64, 0, len(s.jobs))
	for id, j := range s.jobs {
		if !want[j.State] {
			continue
		}
		if inmemFinishTime(j).Before(cutoff) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })

	var n int64
	for _, id := range ids {
		if limit > 0 && n >= int64(limit) {
			break
		}
		delete(s.jobs, id)
		delete(s.tags, id)
		delete(s.meta, id)
		n++
	}
	return n, nil
}

// RescueExecuting recovers jobs stuck in [StateExecuting] whose AttemptedAt is
// strictly before olderThan (their runner most likely crashed). A stuck job with
// attempts to spare is returned to [StateAvailable] scheduled at now; a stuck job
// that has exhausted its attempts is moved to [StateDiscarded] with DiscardedAt
// set to now. It returns how many jobs were rescued and how many were discarded.
// It implements [RescuableStore].
func (s *InMemoryStore) RescueExecuting(_ context.Context, olderThan time.Time, now time.Time) (rescued, discarded int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.State != StateExecuting {
			continue
		}
		if !j.AttemptedAt.Before(olderThan) {
			continue
		}
		if j.Attempt < j.MaxAttempts {
			j.State = StateAvailable
			j.ScheduledAt = now
			rescued++
		} else {
			j.State = StateDiscarded
			j.DiscardedAt = now
			discarded++
		}
	}
	return rescued, discarded, nil
}

// FindConflict returns a copy of the earliest existing job (by ID) that would
// block a unique insert of job under the resolved rule by, or nil when there is
// no conflict. A candidate conflicts when it is in one of by.States, was inserted
// no earlier than by.Period before now, and matches job on every selected
// by.Field together with any explicit by.Keys. Unset States and Fields take the
// Elixir-Oban defaults. It returns nil, nil when by.Period is non-positive. It
// implements [UniqueStore].
func (s *InMemoryStore) FindConflict(_ context.Context, job *Job, by UniqueBy, now time.Time) (*Job, error) {
	if job == nil || by.Period <= 0 {
		return nil, nil
	}
	by = by.insertWithDefaults()
	states := make(map[State]bool, len(by.States))
	for _, st := range by.States {
		states[st] = true
	}
	cutoff := now.Add(-by.Period)
	want := insertUniqueKey(job, by)

	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]int64, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })

	for _, id := range ids {
		existing := s.jobs[id]
		if !states[existing.State] {
			continue
		}
		if existing.InsertedAt.Before(cutoff) {
			continue
		}
		if insertUniqueKey(existing, by) == want {
			return existing.Clone(), nil
		}
	}
	return nil, nil
}

// SetTagsMeta replaces the stored tags and meta for the job with the given id.
// A nil tags or meta clears the corresponding entry. The values are copied, so
// later mutation of the caller's slice or map does not affect stored state. It
// implements [TaggableStore].
func (s *InMemoryStore) SetTagsMeta(_ context.Context, id int64, tags []string, meta map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tags == nil {
		s.tags = make(map[int64][]string)
	}
	if s.meta == nil {
		s.meta = make(map[int64]map[string]any)
	}
	if len(tags) == 0 {
		delete(s.tags, id)
	} else {
		s.tags[id] = append([]string(nil), tags...)
	}
	if len(meta) == 0 {
		delete(s.meta, id)
	} else {
		cp := make(map[string]any, len(meta))
		for k, v := range meta {
			cp[k] = v
		}
		s.meta[id] = cp
	}
	return nil
}

// TagsMeta returns copies of the stored tags and meta for the job with the given
// id. Absent entries yield a nil slice and nil map with a nil error. It
// implements [TaggableStore].
func (s *InMemoryStore) TagsMeta(_ context.Context, id int64) (tags []string, meta map[string]any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tags[id]; ok {
		tags = append([]string(nil), t...)
	}
	if m, ok := s.meta[id]; ok {
		meta = make(map[string]any, len(m))
		for k, v := range m {
			meta[k] = v
		}
	}
	return tags, meta, nil
}

// inmemFinishTime returns the timestamp used to age a finished job for pruning:
// CompletedAt for completed jobs, DiscardedAt for discarded or cancelled jobs,
// falling back to InsertedAt when the terminal timestamp is unset.
func inmemFinishTime(j *Job) time.Time {
	switch j.State {
	case StateCompleted:
		if !j.CompletedAt.IsZero() {
			return j.CompletedAt
		}
	case StateDiscarded, StateCancelled:
		if !j.DiscardedAt.IsZero() {
			return j.DiscardedAt
		}
	}
	return j.InsertedAt
}
