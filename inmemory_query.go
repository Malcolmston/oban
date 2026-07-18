package oban

import (
	"context"
	"sort"
)

// Compile-time assertion that InMemoryStore answers filtered queries directly.
var _ ListableStore = (*InMemoryStore)(nil)

// List returns copies of the stored jobs matching filter, ordered by ascending
// ID. It implements [ListableStore], letting [ListJobs] and [CountJobs] query an
// InMemoryStore without a full snapshot copy of non-matching jobs.
func (s *InMemoryStore) List(_ context.Context, filter JobFilter) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if filter.Matches(j) {
			out = append(out, j.Clone())
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

// Len returns the number of jobs currently held by the store, across all states.
func (s *InMemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// Clear removes every job, along with all stored tags and meta, resetting the
// store to empty. The ID counter is not reset, so IDs assigned after Clear do
// not collide with any that a caller may still hold. It is primarily a testing
// convenience for reusing a store between cases.
func (s *InMemoryStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = make(map[int64]*Job)
	s.tags = make(map[int64][]string)
	s.meta = make(map[int64]map[string]any)
}

// CountByQueue returns the number of stored jobs on the given queue, across all
// states.
func (s *InMemoryStore) CountByQueue(queue string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.Queue == queue {
			n++
		}
	}
	return n
}

// States returns a histogram of the stored jobs by [State]. States with no jobs
// are omitted from the returned map, which the caller may freely mutate.
func (s *InMemoryStore) States() map[State]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[State]int)
	for _, j := range s.jobs {
		out[j.State]++
	}
	return out
}
