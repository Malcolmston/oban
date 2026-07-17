package oban

import (
	"context"
	"sync"
	"time"
)

// EventName identifies a kind of event published on a [Recorder]. The defined
// constants form a superset of the [Telemetry] hooks, covering the full job
// lifecycle plus engine, plugin and queue-control events.
type EventName string

const (
	// JobStart is emitted just before a job attempt runs.
	JobStart EventName = "job.start"
	// JobStop is emitted after a job attempt completes successfully.
	JobStop EventName = "job.stop"
	// JobException is emitted after a job attempt fails with an error.
	JobException EventName = "job.exception"
	// JobRetry is emitted when a failed job is scheduled for another attempt.
	JobRetry EventName = "job.retry"
	// JobDiscard is emitted when a job is discarded and will not run again.
	JobDiscard EventName = "job.discard"
	// JobCancel is emitted when a job is cancelled before completion.
	JobCancel EventName = "job.cancel"
	// JobSnooze is emitted when a job snoozes itself to run later.
	JobSnooze EventName = "job.snooze"
	// EngineInsert is emitted when the engine inserts one or more jobs.
	EngineInsert EventName = "engine.insert"
	// PluginPrune is emitted when a pruning plugin deletes old jobs.
	PluginPrune EventName = "plugin.prune"
	// PluginRescue is emitted when a rescuing plugin resets stuck jobs.
	PluginRescue EventName = "plugin.rescue"
	// QueuePause is emitted when a queue is paused.
	QueuePause EventName = "queue.pause"
	// QueueResume is emitted when a paused queue is resumed.
	QueueResume EventName = "queue.resume"
)

// Event is a single structured record published on a [Recorder]. Only the
// fields relevant to a given [EventName] are populated; the rest carry their
// zero values.
type Event struct {
	// Name is the kind of event.
	Name EventName
	// At is the time the event occurred.
	At time.Time
	// Job is the job the event concerns, or nil for events not tied to a job.
	Job *Job
	// Queue is the affected queue name.
	Queue string
	// Worker is the registry name of the job's worker.
	Worker string
	// Attempt is the 1-based attempt number at the time of the event.
	Attempt int
	// MaxAttempts is the job's maximum attempt count.
	MaxAttempts int
	// Duration is the wall-clock duration of a completed or failed attempt.
	Duration time.Duration
	// Error is the failure cause for exception, discard or cancel events.
	Error error
	// Tags is an optional set of free-form labels attached by the emitter.
	Tags []string
	// Meta holds optional structured metadata attached by the emitter.
	Meta map[string]any
}

// EventHandler consumes an [Event] published on a [Recorder]. Handlers run
// synchronously on the goroutine that calls Emit and must not block for long.
type EventHandler func(Event)

// Recorder is a concurrency-safe, multi-subscriber event stream. Handlers are
// attached with [Recorder.Attach] and events are published with
// [Recorder.Emit]; every attached handler receives every event. A Recorder also
// bridges into the engine via [Recorder.Middleware] and [Recorder.AsTelemetry].
//
// The zero value is not ready for use; construct one with [NewRecorder].
type Recorder struct {
	mu      sync.Mutex
	nextID  uint64
	handler map[uint64]EventHandler
	counts  map[EventName]int
}

// NewRecorder returns an empty [Recorder] with no subscribers.
func NewRecorder() *Recorder {
	return &Recorder{
		handler: make(map[uint64]EventHandler),
		counts:  make(map[EventName]int),
	}
}

// Attach registers h to receive every subsequent [Event]. It returns a detach
// function that removes h; detach is idempotent and safe to call from any
// goroutine. A nil handler is ignored and detach is a no-op.
func (r *Recorder) Attach(h EventHandler) (detach func()) {
	if h == nil {
		return func() {}
	}
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.handler[id] = h
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.handler, id)
			r.mu.Unlock()
		})
	}
}

// Emit publishes ev to every attached handler and increments the cumulative
// counter for ev.Name. Handlers are invoked synchronously, in an unspecified
// order, on the calling goroutine. It is safe for concurrent use.
func (r *Recorder) Emit(ev Event) {
	r.mu.Lock()
	r.counts[ev.Name]++
	handlers := make([]EventHandler, 0, len(r.handler))
	for _, h := range r.handler {
		handlers = append(handlers, h)
	}
	r.mu.Unlock()

	for _, h := range handlers {
		h(ev)
	}
}

// Counts returns a snapshot of the cumulative number of events emitted per
// [EventName]. The returned map is a copy and may be freely mutated by the
// caller.
func (r *Recorder) Counts() map[EventName]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[EventName]int, len(r.counts))
	for name, n := range r.counts {
		out[name] = n
	}
	return out
}

// Middleware returns a [Middleware] that emits a [JobStart] event before the
// wrapped handler runs and a [JobStop] (on success) or [JobException] (on
// error) event afterwards, populating Duration, Attempt, Queue and Worker from
// the executing [Job]. If clock is nil, [SystemClock] is used to measure the
// attempt duration.
func (r *Recorder) Middleware(clock Clock) Middleware {
	if clock == nil {
		clock = SystemClock{}
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, job *Job) error {
			start := clock.Now()
			r.Emit(eventsForJob(JobStart, start, job, 0, nil))

			err := next(ctx, job)

			end := clock.Now()
			dur := end.Sub(start)
			if err != nil {
				r.Emit(eventsForJob(JobException, end, job, dur, err))
			} else {
				r.Emit(eventsForJob(JobStop, end, job, dur, nil))
			}
			return err
		}
	}
}

// AsTelemetry returns a [Telemetry] whose OnStart, OnComplete and OnError hooks
// forward into [Recorder.Emit] as [JobStart], [JobStop] and [JobException]
// events. This lets a Recorder be installed directly as Config.Telemetry and
// participate in the unmodified engine. The clock supplies the timestamp for
// the start event; if nil, [SystemClock] is used.
func (r *Recorder) AsTelemetry(clock Clock) *Telemetry {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Telemetry{
		OnStart: func(ctx context.Context, job *Job) {
			r.Emit(eventsForJob(JobStart, clock.Now(), job, 0, nil))
		},
		OnComplete: func(ctx context.Context, job *Job, dur time.Duration) {
			r.Emit(eventsForJob(JobStop, clock.Now(), job, dur, nil))
		},
		OnError: func(ctx context.Context, job *Job, err error, dur time.Duration) {
			r.Emit(eventsForJob(JobException, clock.Now(), job, dur, err))
		},
	}
}

// eventsForJob builds an [Event] for a job-lifecycle name, copying the queue,
// worker and attempt fields out of job. A nil job yields an event with those
// fields left at their zero values.
func eventsForJob(name EventName, at time.Time, job *Job, dur time.Duration, err error) Event {
	ev := Event{
		Name:     name,
		At:       at,
		Job:      job,
		Duration: dur,
		Error:    err,
	}
	if job != nil {
		ev.Queue = job.Queue
		ev.Worker = job.Worker
		ev.Attempt = job.Attempt
		ev.MaxAttempts = job.MaxAttempts
	}
	return ev
}
