package oban

import (
	"context"
	"sort"
	"time"
)

// JobFilter selects a subset of stored jobs by common attributes. A zero
// JobFilter matches every job; each set field narrows the match, and all set
// fields must hold (logical AND). It is used by [ListJobs], [CountJobs] and the
// testing helpers [AssertEnqueued] and [RefuteEnqueued], and mirrors the keyword
// filters accepted by Elixir Oban's testing and query helpers.
type JobFilter struct {
	// Queue, when non-empty, requires the job's Queue to match exactly.
	Queue string
	// Worker, when non-empty, requires the job's Worker to match exactly.
	Worker string
	// States, when non-empty, requires the job's State to be one of the listed
	// states.
	States []State
	// MinPriority, when non-nil, requires Priority >= *MinPriority.
	MinPriority *int
	// MaxPriority, when non-nil, requires Priority <= *MaxPriority.
	MaxPriority *int
	// InsertedAfter, when non-zero, requires InsertedAt to be strictly after it.
	InsertedAfter time.Time
	// InsertedBefore, when non-zero, requires InsertedAt to be strictly before
	// it.
	InsertedBefore time.Time
}

// Matches reports whether job satisfies every set field of the filter. A nil
// job never matches.
func (f JobFilter) Matches(job *Job) bool {
	if job == nil {
		return false
	}
	if f.Queue != "" && job.Queue != f.Queue {
		return false
	}
	if f.Worker != "" && job.Worker != f.Worker {
		return false
	}
	if len(f.States) > 0 {
		ok := false
		for _, st := range f.States {
			if job.State == st {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.MinPriority != nil && job.Priority < *f.MinPriority {
		return false
	}
	if f.MaxPriority != nil && job.Priority > *f.MaxPriority {
		return false
	}
	if !f.InsertedAfter.IsZero() && !job.InsertedAt.After(f.InsertedAfter) {
		return false
	}
	if !f.InsertedBefore.IsZero() && !job.InsertedAt.Before(f.InsertedBefore) {
		return false
	}
	return true
}

// ListableStore is the optional capability a [Store] implements to answer
// filtered job queries directly. Stores that cannot query efficiently may omit
// it, in which case [ListJobs] and [CountJobs] fall back to scanning an
// [InMemoryStore]-style snapshot when one is available.
type ListableStore interface {
	// List returns copies of the stored jobs matching filter, ordered by
	// ascending ID.
	List(ctx context.Context, filter JobFilter) ([]*Job, error)
}

// snapshotStore is the minimal capability [ListJobs] uses as a fallback for
// stores that do not implement [ListableStore]: any store exposing all of its
// jobs (notably [InMemoryStore.All]) can be filtered in memory.
type snapshotStore interface {
	All() []*Job
}

// ListJobs returns copies of the jobs in store matching filter, ordered by
// ascending ID. When store implements [ListableStore] its List method is used;
// otherwise, when store exposes an All method (as [InMemoryStore] does), the
// snapshot is filtered in memory. A store providing neither capability yields a
// nil slice and a nil error.
func ListJobs(ctx context.Context, store Store, filter JobFilter) ([]*Job, error) {
	if ls, ok := store.(ListableStore); ok {
		return ls.List(ctx, filter)
	}
	if snap, ok := store.(snapshotStore); ok {
		all := snap.All()
		out := make([]*Job, 0, len(all))
		for _, j := range all {
			if filter.Matches(j) {
				out = append(out, j)
			}
		}
		sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
		return out, nil
	}
	return nil, nil
}

// CountJobs returns the number of jobs in store matching filter, using the same
// resolution rules as [ListJobs].
func CountJobs(ctx context.Context, store Store, filter JobFilter) (int, error) {
	jobs, err := ListJobs(ctx, store, filter)
	if err != nil {
		return 0, err
	}
	return len(jobs), nil
}
