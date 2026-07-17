// Package oban is a background job processing system for Go in the style of
// Elixir's Oban and Ruby's Sidekiq.
//
// Jobs are enqueued into a [Store], picked up by an [Oban] engine that runs one
// or more named queues at a configured concurrency, and executed by a [Worker]
// resolved from a name→worker [Registry]. Failed jobs are retried with
// exponential backoff and jitter until they succeed or exhaust their attempts,
// at which point they are discarded. The engine also supports unique-job
// de-duplication, cron-scheduled periodic jobs, middleware/telemetry hooks, and
// graceful draining shutdown.
//
// # Concepts
//
//   - [Job]: a unit of work with a queue, JSON arguments, attempt bookkeeping,
//     priority, schedule, uniqueness settings and error history. Build one with
//     [NewJob].
//   - [Worker]: the code that runs a job, via Perform(ctx, job). Register
//     workers by name in a [Registry] (or directly on the engine).
//   - [Store]: persistence and the transitions the engine drives. [InMemoryStore]
//     is a complete implementation; implement the interface to back Oban with a
//     database.
//   - [Oban]: the engine. It polls queues, executes workers with a per-attempt
//     timeout, retries with [Backoff], and runs [Periodic] cron jobs.
//
// # Quick start
//
//	store := oban.NewInMemoryStore()
//	engine, _ := oban.New(oban.Config{
//		Store:  store,
//		Queues: map[string]int{"default": 5},
//	})
//	engine.RegisterFunc("email", func(ctx context.Context, job *oban.Job) error {
//		var args struct{ To string }
//		_ = job.UnmarshalArgs(&args)
//		return sendEmail(ctx, args.To)
//	})
//
//	ctx := context.Background()
//	_ = engine.Start(ctx)
//	job, _ := oban.NewJob("email", map[string]string{"To": "a@b.com"})
//	_, _, _ = engine.Enqueue(ctx, job)
//	// ... later, on shutdown:
//	_ = engine.Stop(context.Background())
//
// # Determinism and testing
//
// The engine owns time through an injectable [Clock]; all store methods that
// depend on the current time receive it explicitly. Retry delays come from a
// [Backoff] whose jitter source can be seeded, and cron scheduling is pure via
// [Schedule.Next]. This makes scheduling, backoff and uniqueness fully testable
// without real sleeps.
//
// # Persistence
//
// [InMemoryStore] keeps everything in process memory and is ideal for tests and
// single-process use. For durable, multi-process deployments, implement [Store]
// against a database; the interface documentation states the atomicity and
// ordering guarantees each method must provide (notably SELECT ... FOR UPDATE
// SKIP LOCKED semantics for FetchAvailable).
package oban
