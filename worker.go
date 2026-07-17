package oban

import (
	"context"
	"fmt"
	"sync"
)

// Worker executes a single [Job]. Perform is called with a context that carries
// the per-attempt timeout and is cancelled if the engine is force-stopped.
//
// A nil error marks the job completed. A non-nil error triggers a retry (if
// attempts remain) or discards the job. Perform should honor ctx cancellation
// for long-running work.
type Worker interface {
	Perform(ctx context.Context, job *Job) error
}

// WorkerFunc adapts a plain function to the [Worker] interface.
type WorkerFunc func(ctx context.Context, job *Job) error

// Perform calls f(ctx, job).
func (f WorkerFunc) Perform(ctx context.Context, job *Job) error {
	return f(ctx, job)
}

// Registry maps worker names to [Worker] implementations. It is safe for
// concurrent use.
type Registry struct {
	mu      sync.RWMutex
	workers map[string]Worker
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{workers: make(map[string]Worker)}
}

// Register associates name with w. It panics if name is empty, w is nil, or
// name is already registered, since these are programmer errors that should
// surface at startup.
func (r *Registry) Register(name string, w Worker) {
	if name == "" {
		panic("oban: cannot register worker with empty name")
	}
	if w == nil {
		panic("oban: cannot register nil worker for " + name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers == nil {
		r.workers = make(map[string]Worker)
	}
	if _, ok := r.workers[name]; ok {
		panic(fmt.Sprintf("oban: worker %q already registered", name))
	}
	r.workers[name] = w
}

// RegisterFunc is a convenience for registering a [WorkerFunc].
func (r *Registry) RegisterFunc(name string, f func(ctx context.Context, job *Job) error) {
	r.Register(name, WorkerFunc(f))
}

// Get returns the worker registered under name.
func (r *Registry) Get(name string) (Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[name]
	return w, ok
}

// Names returns the registered worker names in unspecified order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.workers))
	for name := range r.workers {
		names = append(names, name)
	}
	return names
}
