package oban

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Default engine settings applied when a [Config] field is left zero.
const (
	// DefaultConcurrency is the per-queue concurrency used when Config.Queues is
	// empty.
	DefaultConcurrency = 10
	// DefaultPollInterval is how often each queue polls the store for work.
	DefaultPollInterval = 100 * time.Millisecond
	// DefaultCronInterval is how often the cron scheduler checks for due jobs.
	DefaultCronInterval = time.Second
	// DefaultJobTimeout bounds a single attempt of a job.
	DefaultJobTimeout = 30 * time.Second
)

// Config configures an [Oban] engine. Only Store is effectively required; every
// other field has a sensible default.
type Config struct {
	// Store persists jobs. Defaults to a new InMemoryStore.
	Store Store

	// Registry resolves worker names. Defaults to an empty registry; register
	// workers with Oban.Register.
	Registry *Registry

	// Queues maps queue name to its maximum concurrency. Defaults to
	// {DefaultQueue: DefaultConcurrency}.
	Queues map[string]int

	// Backoff computes retry delays. Defaults to an ExponentialBackoff.
	Backoff Backoff

	// Clock supplies the current time. Defaults to SystemClock.
	Clock Clock

	// Middleware wraps every attempt. The first element is the outermost
	// wrapper (see [Middleware]).
	Middleware []Middleware

	// Telemetry, if set, is installed as the innermost middleware so its timing
	// measures the worker itself.
	Telemetry *Telemetry

	// Periodic declares cron-scheduled jobs.
	Periodic []Periodic

	// PollInterval is how often queues poll for work. Defaults to
	// DefaultPollInterval.
	PollInterval time.Duration

	// CronInterval is how often the scheduler checks for due periodic jobs.
	// Defaults to DefaultCronInterval.
	CronInterval time.Duration

	// JobTimeout bounds each attempt. Defaults to DefaultJobTimeout. A job may
	// override it per-queue through QueueTimeouts.
	JobTimeout time.Duration

	// QueueTimeouts optionally overrides JobTimeout for specific queues.
	QueueTimeouts map[string]time.Duration

	// ErrorHandler, if set, is called for every failed or discarded attempt.
	ErrorHandler func(job *Job, err error)

	// Logger receives operational messages (store errors, unhandled failures).
	// Defaults to the standard logger. Set to a discarding logger to silence.
	Logger *log.Logger
}

// Oban is a background job engine. It runs a set of queues, each polling a
// [Store] and executing registered [Worker]s with bounded concurrency, retries
// with backoff, unique-job de-duplication and cron scheduling.
//
// Construct one with [New], register workers, then [Oban.Start] it. Call
// [Oban.Stop] for a graceful, draining shutdown.
type Oban struct {
	store         Store
	registry      *Registry
	queues        map[string]int
	backoff       Backoff
	clock         Clock
	periodics     []Periodic
	pollInterval  time.Duration
	cronInterval  time.Duration
	jobTimeout    time.Duration
	queueTimeouts map[string]time.Duration
	errorHandler  func(job *Job, err error)
	logger        *log.Logger

	handler Handler // middleware-wrapped worker invocation

	mu         sync.Mutex
	started    bool
	stopped    bool
	sems       map[string]chan struct{}
	pollCtx    context.Context
	pollCancel context.CancelFunc
	jobCtx     context.Context
	jobCancel  context.CancelFunc
	loopWG     sync.WaitGroup // producers and cron
	jobWG      sync.WaitGroup // in-flight attempts
}

// New builds an engine from cfg, applying defaults. It returns an error only if
// cfg is internally inconsistent (e.g. a non-positive queue concurrency).
func New(cfg Config) (*Oban, error) {
	o := &Oban{
		store:         cfg.Store,
		registry:      cfg.Registry,
		queues:        cfg.Queues,
		backoff:       cfg.Backoff,
		clock:         cfg.Clock,
		periodics:     cfg.Periodic,
		pollInterval:  cfg.PollInterval,
		cronInterval:  cfg.CronInterval,
		jobTimeout:    cfg.JobTimeout,
		queueTimeouts: cfg.QueueTimeouts,
		errorHandler:  cfg.ErrorHandler,
		logger:        cfg.Logger,
		sems:          make(map[string]chan struct{}),
	}
	if o.store == nil {
		o.store = NewInMemoryStore()
	}
	if o.registry == nil {
		o.registry = NewRegistry()
	}
	if len(o.queues) == 0 {
		o.queues = map[string]int{DefaultQueue: DefaultConcurrency}
	}
	if o.backoff == nil {
		o.backoff = &ExponentialBackoff{}
	}
	if o.clock == nil {
		o.clock = SystemClock{}
	}
	if o.pollInterval <= 0 {
		o.pollInterval = DefaultPollInterval
	}
	if o.cronInterval <= 0 {
		o.cronInterval = DefaultCronInterval
	}
	if o.jobTimeout <= 0 {
		o.jobTimeout = DefaultJobTimeout
	}
	if o.logger == nil {
		o.logger = log.Default()
	}

	for name, conc := range o.queues {
		if conc <= 0 {
			return nil, fmt.Errorf("oban: queue %q concurrency must be positive, got %d", name, conc)
		}
		o.sems[name] = make(chan struct{}, conc)
	}
	for i, p := range o.periodics {
		if p.Schedule == nil {
			return nil, fmt.Errorf("oban: periodic[%d]: nil schedule", i)
		}
		if p.Worker == "" {
			return nil, fmt.Errorf("oban: periodic[%d]: empty worker", i)
		}
	}

	// Build the handler chain once: user middleware (outer) then telemetry
	// (inner) then the worker invocation.
	mws := cfg.Middleware
	if cfg.Telemetry != nil {
		mws = append(append([]Middleware(nil), mws...), cfg.Telemetry.Middleware(o.clock))
	}
	o.handler = chain(o.runWorker, mws)

	return o, nil
}

// Register registers a worker on the engine's registry. It is a convenience for
// o.Registry().Register(name, w).
func (o *Oban) Register(name string, w Worker) { o.registry.Register(name, w) }

// RegisterFunc registers a [WorkerFunc] on the engine's registry.
func (o *Oban) RegisterFunc(name string, f func(ctx context.Context, job *Job) error) {
	o.registry.RegisterFunc(name, f)
}

// Registry returns the engine's worker registry.
func (o *Oban) Registry() *Registry { return o.registry }

// Store returns the engine's store.
func (o *Oban) Store() Store { return o.store }

// Enqueue inserts job into the store, stamping its insertion time from the
// engine clock (so unique windows and schedules are deterministic). It returns
// the stored job and whether a new row was inserted; inserted=false indicates
// the job was de-duplicated against an existing one.
func (o *Oban) Enqueue(ctx context.Context, job *Job) (*Job, bool, error) {
	if job == nil {
		return nil, false, errors.New("oban: cannot enqueue nil job")
	}
	if job.InsertedAt.IsZero() {
		job.InsertedAt = o.clock.Now()
	}
	return o.store.Enqueue(ctx, job)
}

// Start begins polling every configured queue and running the cron scheduler.
// It returns immediately; work proceeds in background goroutines until [Oban.Stop]
// is called or ctx is cancelled. Start is not re-entrant.
func (o *Oban) Start(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.started {
		return errors.New("oban: already started")
	}
	if o.stopped {
		return errors.New("oban: engine has been stopped")
	}
	o.started = true

	o.pollCtx, o.pollCancel = context.WithCancel(ctx)
	o.jobCtx, o.jobCancel = context.WithCancel(ctx)

	for name, conc := range o.queues {
		o.loopWG.Add(1)
		go o.runQueue(name, conc)
	}
	if len(o.periodics) > 0 {
		o.loopWG.Add(1)
		go o.runCron()
	}
	return nil
}

// Stop performs a graceful shutdown: it stops queues from fetching new work,
// then waits for in-flight attempts to finish. If ctx is cancelled before the
// drain completes, in-flight attempts are force-cancelled (their contexts are
// cancelled) and Stop returns ctx.Err once they unwind.
func (o *Oban) Stop(ctx context.Context) error {
	o.mu.Lock()
	if !o.started || o.stopped {
		o.mu.Unlock()
		return nil
	}
	o.stopped = true
	o.mu.Unlock()

	// Stop producers and cron, then wait for them to exit so no new attempts
	// are launched.
	o.pollCancel()
	o.loopWG.Wait()

	// Drain in-flight attempts.
	done := make(chan struct{})
	go func() {
		o.jobWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		o.jobCancel()
		return nil
	case <-ctx.Done():
		// Force-cancel remaining attempts and wait for them to unwind.
		o.jobCancel()
		<-done
		return ctx.Err()
	}
}

// runQueue is the producer loop for a single queue.
func (o *Oban) runQueue(queue string, _ int) {
	defer o.loopWG.Done()
	sem := o.sems[queue]
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-o.pollCtx.Done():
			return
		case <-ticker.C:
			o.dispatch(queue, sem)
		}
	}
}

// dispatch fetches as many jobs as there is free capacity and launches them.
func (o *Oban) dispatch(queue string, sem chan struct{}) {
	free := cap(sem) - len(sem)
	if free <= 0 {
		return
	}
	jobs, err := o.store.FetchAvailable(o.pollCtx, queue, free, o.clock.Now())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			o.logf("oban: fetch on queue %q: %v", queue, err)
		}
		return
	}
	for _, job := range jobs {
		sem <- struct{}{} // never blocks: we reserved free slots and are the sole producer
		o.jobWG.Add(1)
		go o.execute(job, sem)
	}
}

// execute runs a single attempt of job and applies the result (complete, retry
// or discard).
func (o *Oban) execute(job *Job, sem chan struct{}) {
	defer o.jobWG.Done()
	defer func() { <-sem }()

	if _, ok := o.registry.Get(job.Worker); !ok {
		o.finishDiscard(job, fmt.Errorf("oban: no worker registered for %q", job.Worker))
		return
	}

	ctx, cancel := context.WithTimeout(o.jobCtx, o.timeoutFor(job.Queue))
	defer cancel()

	err := o.handler(ctx, job)
	now := o.clock.Now()

	switch {
	case err == nil:
		if e := o.store.Complete(o.jobCtx, job.ID, now); e != nil {
			o.logf("oban: complete job %d: %v", job.ID, e)
		}
	case job.Attempt >= job.MaxAttempts:
		o.finishDiscard(job, err)
	default:
		delay := o.backoff.Next(job.Attempt)
		if e := o.store.Retry(o.jobCtx, job.ID, now.Add(delay), err, now); e != nil {
			o.logf("oban: retry job %d: %v", job.ID, e)
		}
		o.notifyError(job, err)
	}
}

// finishDiscard moves job to discarded and notifies the error handler.
func (o *Oban) finishDiscard(job *Job, err error) {
	if e := o.store.Discard(o.jobCtx, job.ID, err, o.clock.Now()); e != nil {
		o.logf("oban: discard job %d: %v", job.ID, e)
	}
	o.notifyError(job, err)
}

// runWorker is the innermost handler: it invokes the registered worker,
// converting panics into errors.
func (o *Oban) runWorker(ctx context.Context, job *Job) (err error) {
	w, ok := o.registry.Get(job.Worker)
	if !ok {
		return fmt.Errorf("oban: no worker registered for %q", job.Worker)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("oban: worker %q panicked: %v", job.Worker, r)
		}
	}()
	return w.Perform(ctx, job)
}

// runCron drives the periodic scheduler using a min-heap keyed by next fire
// time.
func (o *Oban) runCron() {
	defer o.loopWG.Done()

	h := &cronHeap{}
	now := o.clock.Now()
	for _, p := range o.periodics {
		heap.Push(h, &cronEntry{periodic: p, next: p.Schedule.Next(now)})
	}
	heap.Init(h)

	ticker := time.NewTicker(o.cronInterval)
	defer ticker.Stop()
	for {
		select {
		case <-o.pollCtx.Done():
			return
		case <-ticker.C:
			o.fireDue(h)
		}
	}
}

// fireDue enqueues every periodic job whose next time has passed and reschedules
// it.
func (o *Oban) fireDue(h *cronHeap) {
	now := o.clock.Now()
	for h.Len() > 0 && !(*h)[0].next.After(now) {
		entry := heap.Pop(h).(*cronEntry)
		o.enqueuePeriodic(entry.periodic)
		entry.next = entry.periodic.Schedule.Next(now)
		heap.Push(h, entry)
	}
}

// enqueuePeriodic builds and enqueues a job for a periodic entry.
func (o *Oban) enqueuePeriodic(p Periodic) {
	job, err := NewJob(p.Worker, p.Args, p.Options...)
	if err != nil {
		o.logf("oban: build periodic job for %q: %v", p.Worker, err)
		return
	}
	if _, _, err := o.Enqueue(o.pollCtx, job); err != nil {
		if !errors.Is(err, context.Canceled) {
			o.logf("oban: enqueue periodic job for %q: %v", p.Worker, err)
		}
	}
}

// timeoutFor returns the per-attempt timeout for queue.
func (o *Oban) timeoutFor(queue string) time.Duration {
	if d, ok := o.queueTimeouts[queue]; ok && d > 0 {
		return d
	}
	return o.jobTimeout
}

// notifyError reports a failed attempt via the error handler or logger.
func (o *Oban) notifyError(job *Job, err error) {
	if o.errorHandler != nil {
		o.errorHandler(job, err)
		return
	}
	o.logf("oban: job %d (%s) failed on attempt %d/%d: %v", job.ID, job.Worker, job.Attempt, job.MaxAttempts, err)
}

// logf writes an operational message to the configured logger.
func (o *Oban) logf(format string, args ...any) {
	if o.logger != nil {
		o.logger.Printf(format, args...)
	}
}
