package oban

import "time"

// Periodic declares a job to be enqueued automatically on a cron [Schedule].
// The engine enqueues a fresh job from Worker/Args/Options at every tick of
// Schedule. Combine Periodic with a unique option (see [WithUnique]) if you need
// to guard against overlapping enqueues.
type Periodic struct {
	// Schedule determines the tick times. Required.
	Schedule *Schedule
	// Worker is the registry name of the worker to run. Required.
	Worker string
	// Args are the arguments for each enqueued job (see [NewJob]).
	Args any
	// Options are applied to each enqueued job.
	Options []JobOption
}

// cronEntry is a heap item pairing a [Periodic] with its next fire time.
type cronEntry struct {
	periodic Periodic
	next     time.Time
	index    int // maintained by container/heap
}

// cronHeap is a min-heap of cron entries ordered by next fire time. It
// implements [container/heap.Interface].
type cronHeap []*cronEntry

func (h cronHeap) Len() int { return len(h) }

func (h cronHeap) Less(i, j int) bool { return h[i].next.Before(h[j].next) }

func (h cronHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

// Push implements heap.Interface. x must be a *cronEntry.
func (h *cronHeap) Push(x any) {
	e := x.(*cronEntry)
	e.index = len(*h)
	*h = append(*h, e)
}

// Pop implements heap.Interface, removing and returning the last element.
func (h *cronHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.index = -1
	*h = old[:n-1]
	return e
}
