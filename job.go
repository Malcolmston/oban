package oban

import (
	"encoding/json"
	"fmt"
	"time"
)

// State describes the lifecycle position of a [Job].
//
// The normal progression is:
//
//	scheduled ─┐
//	           ├─▶ available ─▶ executing ─▶ completed
//	           │                     │
//	           └──── retryable ◀─────┤ (error, attempts remain)
//	                                 │
//	                                 └─▶ discarded (error, no attempts left)
//
// A job may also be moved directly to [StateCancelled] by an operator.
type State string

const (
	// StateScheduled marks a job whose ScheduledAt is in the future. It
	// becomes fetchable once that time passes.
	StateScheduled State = "scheduled"
	// StateAvailable marks a job that is ready to be fetched and executed.
	StateAvailable State = "available"
	// StateExecuting marks a job that has been fetched by the engine and is
	// currently running (or crashed mid-run).
	StateExecuting State = "executing"
	// StateRetryable marks a job that failed but still has attempts left. It
	// carries a future ScheduledAt computed from the backoff policy.
	StateRetryable State = "retryable"
	// StateCompleted marks a job that finished successfully.
	StateCompleted State = "completed"
	// StateDiscarded marks a job that exhausted its attempts (or failed
	// permanently) and will not run again.
	StateDiscarded State = "discarded"
	// StateCancelled marks a job cancelled by an operator before completion.
	StateCancelled State = "cancelled"
)

// fetchableStates are the states from which a job may be picked up for
// execution once its ScheduledAt has passed.
var fetchableStates = map[State]bool{
	StateAvailable: true,
	StateScheduled: true,
	StateRetryable: true,
}

// unfinishedStates are the states considered "in flight" for the purpose of
// unique-job de-duplication. A job in any of these states blocks the insertion
// of a duplicate within the unique period.
var unfinishedStates = map[State]bool{
	StateAvailable: true,
	StateScheduled: true,
	StateExecuting: true,
	StateRetryable: true,
}

// DefaultMaxAttempts is the number of attempts a [Job] is given when none is
// specified.
const DefaultMaxAttempts = 20

// DefaultQueue is the queue a [Job] is placed on when none is specified.
const DefaultQueue = "default"

// AttemptError records a single failed attempt of a [Job].
type AttemptError struct {
	// Attempt is the 1-based attempt number that produced this error.
	Attempt int `json:"attempt"`
	// At is the time the error was recorded.
	At time.Time `json:"at"`
	// Message is the string form of the error returned by the worker.
	Message string `json:"message"`
}

// Job is a unit of background work. Jobs are enqueued into a [Store], fetched by
// the engine for a specific queue, and executed by the [Worker] registered
// under Worker.
//
// A zero Job is not valid; construct jobs with [NewJob] which applies defaults
// and marshals arguments to JSON.
type Job struct {
	// ID is a unique identifier assigned by the Store on enqueue.
	ID int64 `json:"id"`

	// Queue is the named queue the job belongs to. Defaults to DefaultQueue.
	Queue string `json:"queue"`

	// Worker is the registry name of the worker that will execute the job.
	Worker string `json:"worker"`

	// Args holds the JSON-encoded arguments passed to the worker. Use
	// [Job.UnmarshalArgs] to decode them.
	Args json.RawMessage `json:"args"`

	// MaxAttempts is the maximum number of times the job may run before it is
	// discarded. Defaults to DefaultMaxAttempts.
	MaxAttempts int `json:"max_attempts"`

	// Attempt is the 1-based count of the current (or most recent) run. It is
	// zero before the first fetch and incremented each time the job is fetched.
	Attempt int `json:"attempt"`

	// Priority orders jobs within a queue; lower values run first. Defaults to
	// 0. Ties are broken by ScheduledAt then ID.
	Priority int `json:"priority"`

	// State is the current lifecycle state of the job.
	State State `json:"state"`

	// ScheduledAt is the earliest time the job may run. Jobs with a future
	// ScheduledAt are not fetched until it passes.
	ScheduledAt time.Time `json:"scheduled_at"`

	// InsertedAt is the time the job was enqueued.
	InsertedAt time.Time `json:"inserted_at"`

	// AttemptedAt is the time of the most recent fetch, or the zero value if
	// the job has never run.
	AttemptedAt time.Time `json:"attempted_at,omitempty"`

	// CompletedAt is the time the job completed successfully, or the zero value.
	CompletedAt time.Time `json:"completed_at,omitempty"`

	// DiscardedAt is the time the job was discarded, or the zero value.
	DiscardedAt time.Time `json:"discarded_at,omitempty"`

	// UniqueKey, when non-empty, enables unique-job de-duplication. An enqueue
	// is skipped if another unfinished job with the same Queue, Worker and
	// UniqueKey was inserted within UniquePeriod.
	UniqueKey string `json:"unique_key,omitempty"`

	// UniquePeriod is the window over which UniqueKey de-duplicates. It is only
	// consulted when UniqueKey is non-empty.
	UniquePeriod time.Duration `json:"unique_period,omitempty"`

	// LastError is the message of the most recent failed attempt.
	LastError string `json:"last_error,omitempty"`

	// Errors is the full history of failed attempts, oldest first.
	Errors []AttemptError `json:"errors,omitempty"`

	// scheduleIn holds a relative delay set by WithScheduleIn. It is unexported
	// (and thus not serialized) and is resolved to ScheduledAt at enqueue time.
	scheduleIn    time.Duration
	hasScheduleIn bool
}

// JobOption customizes a [Job] built with [NewJob].
type JobOption func(*Job)

// WithQueue sets the queue for the job.
func WithQueue(queue string) JobOption {
	return func(j *Job) { j.Queue = queue }
}

// WithMaxAttempts sets the maximum number of attempts for the job.
func WithMaxAttempts(n int) JobOption {
	return func(j *Job) { j.MaxAttempts = n }
}

// WithPriority sets the priority for the job; lower values run first.
func WithPriority(p int) JobOption {
	return func(j *Job) { j.Priority = p }
}

// WithScheduledAt schedules the job to run no earlier than t.
func WithScheduledAt(t time.Time) JobOption {
	return func(j *Job) {
		j.ScheduledAt = t
		j.State = StateScheduled
	}
}

// WithScheduleIn schedules the job to run no earlier than d from its insertion
// time. The delay is resolved against the enqueue time by the engine or store.
func WithScheduleIn(d time.Duration) JobOption {
	return func(j *Job) {
		j.scheduleIn = d
		j.hasScheduleIn = true
		j.State = StateScheduled
	}
}

// WithUnique enables unique-job de-duplication for the job over the given
// period. Jobs sharing a Queue, Worker and key de-duplicate against one another.
func WithUnique(key string, period time.Duration) JobOption {
	return func(j *Job) {
		j.UniqueKey = key
		j.UniquePeriod = period
	}
}

// NewJob builds a job for the named worker, marshaling args to JSON. If args is
// nil the job is given an empty JSON object. Options are applied in order.
//
// Defaults: Queue=DefaultQueue, MaxAttempts=DefaultMaxAttempts,
// State=StateAvailable.
func NewJob(worker string, args any, opts ...JobOption) (*Job, error) {
	if worker == "" {
		return nil, fmt.Errorf("oban: worker name must not be empty")
	}
	var raw json.RawMessage
	switch v := args.(type) {
	case nil:
		raw = json.RawMessage("{}")
	case json.RawMessage:
		raw = v
	default:
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("oban: marshal args: %w", err)
		}
		raw = b
	}
	j := &Job{
		Queue:       DefaultQueue,
		Worker:      worker,
		Args:        raw,
		MaxAttempts: DefaultMaxAttempts,
		State:       StateAvailable,
	}
	for _, opt := range opts {
		opt(j)
	}
	if j.Queue == "" {
		j.Queue = DefaultQueue
	}
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = DefaultMaxAttempts
	}
	return j, nil
}

// UnmarshalArgs decodes the job's JSON arguments into v.
func (j *Job) UnmarshalArgs(v any) error {
	if len(j.Args) == 0 {
		return nil
	}
	return json.Unmarshal(j.Args, v)
}

// Clone returns a deep copy of the job, safe to hand to callers without
// exposing the store's internal pointer.
func (j *Job) Clone() *Job {
	if j == nil {
		return nil
	}
	cp := *j
	if j.Args != nil {
		cp.Args = append(json.RawMessage(nil), j.Args...)
	}
	if j.Errors != nil {
		cp.Errors = append([]AttemptError(nil), j.Errors...)
	}
	return &cp
}

// recordError appends err to the job's error history and updates LastError.
func (j *Job) recordError(err error, at time.Time) {
	msg := err.Error()
	j.LastError = msg
	j.Errors = append(j.Errors, AttemptError{
		Attempt: j.Attempt,
		At:      at,
		Message: msg,
	})
}
