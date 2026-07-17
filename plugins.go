package oban

import (
	"container/heap"
	"context"
	"log"
	"sync"
	"time"
)

// Plugin is a background maintenance task that runs alongside the [Oban] engine
// as its own goroutine. Plugins operate against a [Store] (through a narrow
// capability interface) or an [Enqueuer] and never require changes to the engine
// itself.
//
// A Plugin's Start must return promptly, launching whatever background work is
// needed; Stop must cancel that work and block until it has drained or the
// supplied context's deadline elapses. Implementations must be safe to Start and
// Stop at most once each.
type Plugin interface {
	// Name returns a short, stable identifier for the plugin, used in logs.
	Name() string
	// Start launches the plugin's background loop bound to ctx and returns
	// immediately. The loop runs until ctx is cancelled or Stop is called.
	Start(ctx context.Context) error
	// Stop signals the plugin to shut down and blocks until its loop has exited
	// or ctx is done, in which case it returns ctx.Err().
	Stop(ctx context.Context) error
}

// PrunableStore is the capability a [Store] exposes to let the [Pruner] delete
// old finished jobs. It is satisfied by the SQL-backed store.
type PrunableStore interface {
	// DeleteFinishedBefore removes up to limit jobs whose State is one of states
	// and whose terminal timestamp is before cutoff, returning the number
	// deleted.
	DeleteFinishedBefore(ctx context.Context, states []State, cutoff time.Time, limit int) (int64, error)
}

// RescuableStore is the capability a [Store] exposes to let the [Lifeline]
// recover jobs orphaned in [StateExecuting]. It is satisfied by the SQL-backed
// store.
type RescuableStore interface {
	// RescueExecuting moves jobs stuck in StateExecuting since before olderThan
	// back to StateAvailable, or to StateDiscarded when their Attempt has reached
	// MaxAttempts. It returns how many were rescued and how many were discarded.
	RescueExecuting(ctx context.Context, olderThan time.Time, now time.Time) (rescued, discarded int64, err error)
}

// Enqueuer is the capability the [CronPlugin] needs to insert scheduled jobs. It
// is satisfied by *[Oban].
type Enqueuer interface {
	// Enqueue inserts job, returning the stored job and whether a new row was
	// inserted (false indicates unique-job de-duplication).
	Enqueue(ctx context.Context, job *Job) (*Job, bool, error)
}

// Default cadences applied when a plugin config leaves an interval or bound at
// its zero value.
const (
	pluginsDefaultPruneInterval    = 30 * time.Second
	pluginsDefaultPruneLimit       = 10000
	pluginsDefaultLifelineInterval = time.Minute
	pluginsDefaultCronInterval     = time.Second
)

// pluginsDefaultPruneStates are the finished states the [Pruner] deletes when a
// [PrunerConfig] does not name its own.
var pluginsDefaultPruneStates = []State{StateCompleted, StateDiscarded, StateCancelled}

// pluginsBase carries the optional logger shared by every plugin. The
// [Supervisor] injects its logger through pluginsSetLogger so plugins built
// standalone stay silent.
type pluginsBase struct {
	logger *log.Logger
}

// pluginsSetLogger sets the logger used by logf.
func (b *pluginsBase) pluginsSetLogger(l *log.Logger) { b.logger = l }

// logf writes an operational message if a logger is configured.
func (b *pluginsBase) logf(format string, args ...any) {
	if b.logger != nil {
		b.logger.Printf(format, args...)
	}
}

// pluginsLoggable is implemented by every plugin so the [Supervisor] can hand
// down its logger regardless of concrete type.
type pluginsLoggable interface {
	pluginsSetLogger(l *log.Logger)
}

// pluginsLoop is the shared lifecycle of a ticking plugin: an optional one-time
// setup, then a fixed-interval tick until cancelled. It is embedded by each
// plugin, which supplies interval, setup and tick.
type pluginsLoop struct {
	pluginsBase
	interval time.Duration
	setup    func()                    // run once before the first tick; may be nil
	tick     func(ctx context.Context) // run on every interval

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// start launches the loop goroutine bound to a child of ctx. It is idempotent:
// a second call while running is a no-op.
func (lp *pluginsLoop) start(ctx context.Context) {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	if lp.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	lp.cancel = cancel
	lp.done = make(chan struct{})

	interval := lp.interval
	setup := lp.setup
	tick := lp.tick
	done := lp.done

	go func() {
		defer close(done)
		if setup != nil {
			setup()
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick(ctx)
			}
		}
	}()
}

// stop cancels the loop and waits for it to exit, honoring ctx's deadline. It
// returns ctx.Err() if ctx is done before the loop drains.
func (lp *pluginsLoop) stop(ctx context.Context) error {
	lp.mu.Lock()
	cancel := lp.cancel
	done := lp.done
	lp.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Supervisor starts and stops a set of [Plugin]s as a unit, tracking their
// goroutines with a WaitGroup so shutdown can drain them under a deadline.
type Supervisor struct {
	clock   Clock
	logger  *log.Logger
	plugins []Plugin

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewSupervisor returns a Supervisor over plugins. The clock is retained for
// plugins and callers that need a shared time source, and logger (if non-nil) is
// pushed to every plugin so their background errors are surfaced.
func NewSupervisor(clock Clock, logger *log.Logger, plugins ...Plugin) *Supervisor {
	return &Supervisor{clock: clock, logger: logger, plugins: plugins}
}

// Start starts every plugin bound to a child of ctx and records each in a
// WaitGroup. It is not re-entrant; a second call while running is a no-op.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	s.started = true

	var sctx context.Context
	sctx, s.cancel = context.WithCancel(ctx)

	for _, p := range s.plugins {
		if s.logger != nil {
			if l, ok := p.(pluginsLoggable); ok {
				l.pluginsSetLogger(s.logger)
			}
		}
		if err := p.Start(sctx); err != nil {
			return err
		}
		s.wg.Add(1)
		go func(p Plugin) {
			defer s.wg.Done()
			<-sctx.Done()
		}(p)
	}
	return nil
}

// Stop cancels the supervised context, stops every plugin and waits for the
// WaitGroup to drain. If ctx is done before every plugin has exited, Stop
// returns ctx.Err().
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		for _, p := range s.plugins {
			_ = p.Stop(ctx)
		}
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PrunerConfig configures a [Pruner].
type PrunerConfig struct {
	// Interval is how often the pruner runs. Defaults to 30s when non-positive.
	Interval time.Duration
	// MaxAge is how old a finished job must be, relative to the clock, before it
	// is eligible for deletion.
	MaxAge time.Duration
	// States are the finished states to prune. Defaults to completed, discarded
	// and cancelled when empty.
	States []State
	// Limit bounds how many jobs are deleted per run. Defaults to 10000 when
	// non-positive.
	Limit int
}

// Pruner is a [Plugin] that periodically deletes old finished jobs
// ([StateCompleted], [StateDiscarded] and [StateCancelled] by default) from a
// [PrunableStore], keeping table sizes bounded.
type Pruner struct {
	pluginsLoop
	store PrunableStore
	clock Clock
	cfg   PrunerConfig
}

// NewPruner returns a Pruner that deletes finished jobs older than cfg.MaxAge
// from store on cfg.Interval, using clock for the age cutoff.
func NewPruner(store PrunableStore, clock Clock, cfg PrunerConfig) *Pruner {
	if cfg.Interval <= 0 {
		cfg.Interval = pluginsDefaultPruneInterval
	}
	p := &Pruner{store: store, clock: clock, cfg: cfg}
	p.interval = cfg.Interval
	p.tick = p.prune
	return p
}

// Name returns the pruner's identifier.
func (p *Pruner) Name() string { return "pruner" }

// Start launches the pruner's background loop.
func (p *Pruner) Start(ctx context.Context) error { p.start(ctx); return nil }

// Stop stops the pruner, honoring ctx's deadline.
func (p *Pruner) Stop(ctx context.Context) error { return p.stop(ctx) }

// prune performs one deletion pass.
func (p *Pruner) prune(ctx context.Context) {
	states := p.cfg.States
	if len(states) == 0 {
		states = pluginsDefaultPruneStates
	}
	limit := p.cfg.Limit
	if limit <= 0 {
		limit = pluginsDefaultPruneLimit
	}
	cutoff := p.clock.Now().Add(-p.cfg.MaxAge)
	if _, err := p.store.DeleteFinishedBefore(ctx, states, cutoff, limit); err != nil {
		p.logf("oban: pruner: %v", err)
	}
}

// LifelineConfig configures a [Lifeline].
type LifelineConfig struct {
	// Interval is how often the lifeline runs. Defaults to 1m when non-positive.
	Interval time.Duration
	// RescueAfter is how long a job may sit in StateExecuting, relative to the
	// clock, before it is rescued or discarded.
	RescueAfter time.Duration
}

// Lifeline is a [Plugin] that periodically recovers jobs orphaned in
// [StateExecuting] — typically because the process running them crashed. Jobs
// stuck longer than cfg.RescueAfter are moved back to [StateAvailable], or to
// [StateDiscarded] once their attempts are exhausted.
type Lifeline struct {
	pluginsLoop
	store RescuableStore
	clock Clock
	cfg   LifelineConfig
}

// NewLifeline returns a Lifeline that rescues executing jobs older than
// cfg.RescueAfter from store on cfg.Interval, using clock for the cutoff.
func NewLifeline(store RescuableStore, clock Clock, cfg LifelineConfig) *Lifeline {
	if cfg.Interval <= 0 {
		cfg.Interval = pluginsDefaultLifelineInterval
	}
	l := &Lifeline{store: store, clock: clock, cfg: cfg}
	l.interval = cfg.Interval
	l.tick = l.rescue
	return l
}

// Name returns the lifeline's identifier.
func (l *Lifeline) Name() string { return "lifeline" }

// Start launches the lifeline's background loop.
func (l *Lifeline) Start(ctx context.Context) error { l.start(ctx); return nil }

// Stop stops the lifeline, honoring ctx's deadline.
func (l *Lifeline) Stop(ctx context.Context) error { return l.stop(ctx) }

// rescue performs one rescue pass.
func (l *Lifeline) rescue(ctx context.Context) {
	now := l.clock.Now()
	olderThan := now.Add(-l.cfg.RescueAfter)
	if _, _, err := l.store.RescueExecuting(ctx, olderThan, now); err != nil {
		l.logf("oban: lifeline: %v", err)
	}
}

// CronPlugin is a [Plugin] that enqueues [Periodic] jobs on their cron
// schedules through an [Enqueuer], detached from Config.Periodic. It reuses the
// same next-fire min-heap the engine uses so behavior is identical whether cron
// runs in-engine or as a standalone plugin.
type CronPlugin struct {
	pluginsLoop
	enq      Enqueuer
	clock    Clock
	entries  []Periodic
	schedule *cronHeap // owned by the loop goroutine after setup
}

// NewCronPlugin returns a CronPlugin that fires entries via enq, checking for
// due schedules every interval (default 1s) and using clock for all timing.
func NewCronPlugin(enq Enqueuer, clock Clock, entries []Periodic, interval time.Duration) *CronPlugin {
	if interval <= 0 {
		interval = pluginsDefaultCronInterval
	}
	c := &CronPlugin{enq: enq, clock: clock, entries: entries}
	c.interval = interval
	c.setup = c.buildHeap
	c.tick = c.fireDue
	return c
}

// Name returns the cron plugin's identifier.
func (c *CronPlugin) Name() string { return "cron" }

// Start launches the cron plugin's background loop.
func (c *CronPlugin) Start(ctx context.Context) error { c.start(ctx); return nil }

// Stop stops the cron plugin, honoring ctx's deadline.
func (c *CronPlugin) Stop(ctx context.Context) error { return c.stop(ctx) }

// buildHeap seeds the next-fire min-heap from the current time. It runs once in
// the loop goroutine before the first tick, so the heap is only ever touched by
// that goroutine.
func (c *CronPlugin) buildHeap() {
	h := &cronHeap{}
	now := c.clock.Now()
	for _, p := range c.entries {
		if p.Schedule == nil {
			continue
		}
		next := p.Schedule.Next(now)
		if next.IsZero() {
			continue // schedule has no future occurrence
		}
		heap.Push(h, &cronEntry{periodic: p, next: next})
	}
	heap.Init(h)
	c.schedule = h
}

// fireDue enqueues every entry whose next time has passed and reschedules it.
func (c *CronPlugin) fireDue(ctx context.Context) {
	if c.schedule == nil {
		return
	}
	now := c.clock.Now()
	for c.schedule.Len() > 0 && !(*c.schedule)[0].next.After(now) {
		entry := heap.Pop(c.schedule).(*cronEntry)
		c.enqueue(ctx, entry.periodic)
		next := entry.periodic.Schedule.Next(now)
		if next.IsZero() {
			continue // drop entries with no further occurrence
		}
		entry.next = next
		heap.Push(c.schedule, entry)
	}
}

// enqueue builds and inserts a single periodic job.
func (c *CronPlugin) enqueue(ctx context.Context, p Periodic) {
	job, err := NewJob(p.Worker, p.Args, p.Options...)
	if err != nil {
		c.logf("oban: cron: build job for %q: %v", p.Worker, err)
		return
	}
	if _, _, err := c.enq.Enqueue(ctx, job); err != nil {
		c.logf("oban: cron: enqueue %q: %v", p.Worker, err)
	}
}
