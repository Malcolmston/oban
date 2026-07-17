# oban

An Oban/Sidekiq-style background job system for Go, using only the standard
library.

`oban` runs named queues at configured concurrency, executes registered workers
with a per-attempt timeout, retries failures with exponential backoff and
jitter, discards jobs that exhaust their attempts, de-duplicates unique jobs,
schedules periodic jobs with cron expressions, wraps execution in
middleware/telemetry, and shuts down gracefully by draining in-flight work.

- Standard library only — no third-party dependencies, no cgo.
- Deterministic and testable: the engine owns time through an injectable clock,
  backoff jitter is seedable, and cron scheduling is pure.
- Pluggable persistence: a complete in-memory `Store` ships in the box, and the
  `Store` interface documents exactly what a database-backed implementation must
  guarantee.

## Install

```sh
go get github.com/malcolmston/oban
```

Requires Go 1.24 or newer.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/malcolmston/oban"
)

func main() {
	engine, err := oban.New(oban.Config{
		Store:  oban.NewInMemoryStore(),
		Queues: map[string]int{"default": 5, "mailers": 2},
		Backoff: &oban.ExponentialBackoff{
			Base:   time.Second,
			Max:    time.Hour,
			Jitter: 0.2,
		},
	})
	if err != nil {
		panic(err)
	}

	// Register a worker by name.
	engine.RegisterFunc("email", func(ctx context.Context, job *oban.Job) error {
		var args struct {
			To string `json:"to"`
		}
		if err := job.UnmarshalArgs(&args); err != nil {
			return err
		}
		fmt.Println("sending email to", args.To)
		return nil
	})

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		panic(err)
	}

	// Enqueue a job.
	job, _ := oban.NewJob("email",
		map[string]string{"to": "ada@example.com"},
		oban.WithQueue("mailers"),
		oban.WithMaxAttempts(5),
		oban.WithPriority(0),
	)
	if _, _, err := engine.Enqueue(ctx, job); err != nil {
		panic(err)
	}

	// ... run your program ...

	// Graceful shutdown: stop fetching and drain in-flight jobs.
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = engine.Stop(shutdown)
}
```

## Concepts

| Type         | Role                                                                                                          |
| ------------ | ------------------------------------------------------------------------------------------------------------ |
| `Job`        | A unit of work: queue, JSON args, attempts, priority, schedule, uniqueness, error history. Build with `NewJob`. |
| `Worker`     | `Perform(ctx, *Job) error`. Register by name in a `Registry` (or on the engine).                             |
| `Store`      | Persistence and state transitions. `InMemoryStore` is complete; implement the interface for a database.       |
| `Oban`       | The engine: polls queues, runs workers, retries, schedules cron jobs.                                          |
| `Backoff`    | Retry delay policy. `ExponentialBackoff` grows the delay and adds jitter.                                      |
| `Schedule`   | A parsed 5-field cron expression; `Next` returns the next fire time.                                           |
| `Middleware` | Wraps every attempt; the first element is the outermost wrapper.                                               |
| `Telemetry`  | `OnStart` / `OnComplete` / `OnError` hooks installed as middleware.                                            |

## Job states

```
scheduled ─┐
           ├─▶ available ─▶ executing ─▶ completed
           │                     │
           └──── retryable ◀─────┤ (error, attempts remain)
                                 │
                                 └─▶ discarded (error, no attempts left)
```

## Job options

```go
oban.NewJob("worker", args,
	oban.WithQueue("mailers"),
	oban.WithMaxAttempts(10),
	oban.WithPriority(1),                      // lower runs first
	oban.WithScheduledAt(time.Now().Add(time.Hour)),
	oban.WithScheduleIn(5*time.Minute),        // relative to enqueue time
	oban.WithUnique("welcome:42", time.Hour),  // de-dup within the period
)
```

## Unique jobs

When a job carries a unique key and period, `Enqueue` returns `inserted == false`
and the existing job if an unfinished job with the same queue, worker and key was
inserted within the period. Finished jobs (completed, discarded, cancelled) do
not block new work.

## Periodic (cron) jobs

```go
engine, _ := oban.New(oban.Config{
	Store:  oban.NewInMemoryStore(),
	Queues: map[string]int{"default": 5},
	Periodic: []oban.Periodic{
		{
			Schedule: oban.MustParseCron("*/15 * * * *"), // every 15 minutes
			Worker:   "refresh_cache",
		},
		{
			Schedule: oban.MustParseCron("0 9 * * mon-fri"), // weekdays at 09:00
			Worker:   "daily_report",
			Options:  []oban.JobOption{oban.WithUnique("daily", 24*time.Hour)},
		},
	},
})
```

The scheduler parses standard five-field cron expressions
(`minute hour day-of-month month day-of-week`) with `*`, lists, ranges, steps,
and month/day names. When both day-of-month and day-of-week are restricted, a
time matches if either does, following the traditional Vixie cron rule.

## Middleware and telemetry

```go
logging := func(next oban.Handler) oban.Handler {
	return func(ctx context.Context, job *oban.Job) error {
		err := next(ctx, job)
		log.Printf("job %d (%s): %v", job.ID, job.Worker, err)
		return err
	}
}

engine, _ := oban.New(oban.Config{
	Store:      oban.NewInMemoryStore(),
	Middleware: []oban.Middleware{logging}, // mw[0] is outermost
	Telemetry: &oban.Telemetry{
		OnComplete: func(ctx context.Context, job *oban.Job, d time.Duration) {
			metrics.Timing("job.duration", d, job.Worker)
		},
	},
})
```

## Persistence

`InMemoryStore` keeps everything in process memory — ideal for tests and
single-process use. For durable, multi-process deployments, implement the
`Store` interface against your database. The interface documentation specifies
the atomicity and ordering guarantees each method must provide, notably that
`FetchAvailable` must lock rows so the same job is never handed to two callers
(e.g. `SELECT ... FOR UPDATE SKIP LOCKED`).

## Testing

The engine owns time through an injectable `Clock`, so scheduling, backoff and
unique windows are deterministic. The package's own test suite drives every
feature — enqueue/process, retry and backoff schedules, discard after max
attempts, unique de-duplication, cron next-time, priority + scheduled ordering,
graceful drain, and middleware ordering — without real sleeps in assertions.

```sh
go test ./...
```

## Versioning

Current version: `0.1.0` (see the `VERSION` file).

## License

See the repository for license details.
