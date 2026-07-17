// Library content for the oban documentation site. Mirrors the shape used by
// the malcolmston/go landing site's data.ts so the sibling sites stay in sync.
export interface Lib {
  id: string; name: string; icon: string; accent: string; pkg: string; node: string;
  repo: string; docs: string; tagline: string; blurb: string; tags: string[];
  features: string[]; node_code: string; go_code: string; integrate: string;
}

export const NODE_ACCENT = '#8cc84b';

export const OBAN: Lib = {
  id:"oban", name:"Oban", icon:'<i class="fa-solid fa-list-check"></i>', accent:"#a855f7",
  pkg:"github.com/malcolmston/oban", node:"sorentwo/oban",
  repo:"https://github.com/malcolmston/oban", docs:"https://malcolmston.github.io/oban/",
  tagline:"Background job processing for Go, standard library only.",
  blurb:"An Oban/Sidekiq-style background job system for Go built on nothing but the standard library. "+
    "An Oban engine runs named queues at configured concurrency, resolves workers by name from a Registry, "+
    "and executes each job with a per-attempt timeout. Failures retry with exponential backoff and jitter "+
    "until they succeed or exhaust their attempts, at which point they are discarded. The engine also "+
    "de-duplicates unique jobs, schedules periodic work with cron expressions, wraps every attempt in "+
    "middleware/telemetry, and shuts down gracefully by draining in-flight jobs. It is deterministic and "+
    "testable: time flows through an injectable clock, backoff jitter is seedable, and cron scheduling is "+
    "pure. A complete in-memory Store ships in the box, and the Store interface documents exactly what a "+
    "database-backed implementation must guarantee.",
  tags:["named queues","exponential backoff","jitter","retries","cron scheduling","unique jobs","graceful drain","middleware/telemetry","in-memory Store","zero dependencies"],
  features:[
    "<code>Oban</code> engine — polls named queues at configured concurrency, built with <code>New(Config{...})</code> and driven by <code>Start</code> / <code>Stop</code>",
    "Jobs as data — a <code>Job</code> carries queue, JSON <code>Args</code>, attempts, priority, schedule and error history; build one with <code>NewJob</code>",
    "Named workers — implement <code>Worker</code> (<code>Perform(ctx, *Job) error</code>) or register a func with <code>RegisterFunc</code> in a <code>Registry</code>",
    "Retries with backoff — the <code>Backoff</code> interface, with <code>ExponentialBackoff</code> growing the delay and adding seedable jitter up to a cap",
    "Discard on exhaustion — jobs that use up <code>MaxAttempts</code> transition to discarded and fire the <code>ErrorHandler</code>",
    "Cron scheduling — declare <code>Periodic</code> jobs against a parsed 5-field <code>Schedule</code>; <code>Schedule.Next</code> is a pure function",
    "Unique jobs — <code>WithUnique(key, period)</code> de-duplicates by queue + worker + key over a time window",
    "Middleware &amp; telemetry — wrap every attempt with a <code>Middleware</code> chain, with <code>Telemetry</code> installed innermost to time the worker",
    "Pluggable persistence — a complete <code>InMemoryStore</code> ships in the box; implement <code>Store</code> (SELECT ... FOR UPDATE SKIP LOCKED semantics) for a database",
    "Deterministic &amp; testable — the engine owns time through an injectable <code>Clock</code>, so scheduling, backoff and uniqueness test without real sleeps",
    "Graceful shutdown — <code>Stop</code> stops fetching and drains in-flight work, honouring the context deadline",
    "Zero dependencies — pure Go standard library, no cgo, nothing to audit but the toolchain"
  ],
  node_code:
`# Elixir Oban — the inspiration for this library.
defmodule MyApp.EmailWorker do
  use Oban.Worker, queue: :mailers, max_attempts: 5

  @impl Oban.Worker
  def perform(%Oban.Job{args: %{"to" => to}}) do
    MyApp.Mailer.deliver(to)
    :ok
  end
end

%{to: "ada@example.com"}
|> MyApp.EmailWorker.new()
|> Oban.insert()`,
  go_code:
`import "github.com/malcolmston/oban"

engine, _ := oban.New(oban.Config{
    Store:  oban.NewInMemoryStore(),
    Queues: map[string]int{"default": 5, "mailers": 2},
})

// Register a worker by name.
engine.RegisterFunc("email", func(ctx context.Context, job *oban.Job) error {
    var args struct{ To string ` + "`" + `json:"to"` + "`" + ` }
    if err := job.UnmarshalArgs(&args); err != nil {
        return err
    }
    return sendEmail(ctx, args.To)
})

_ = engine.Start(ctx)
job, _ := oban.NewJob("email", map[string]string{"to": "ada@example.com"},
    oban.WithQueue("mailers"), oban.WithMaxAttempts(5))
_, _, _ = engine.Enqueue(ctx, job)`,
  integrate:
`<span class="tok-c">// Retry failures with exponential backoff + jitter, capped at an hour.</span>
engine, _ := oban.New(oban.Config{
    Store:   oban.NewInMemoryStore(),
    Queues:  map[string]int{"default": 5},
    Backoff: oban.NewExponentialBackoff(time.Second, time.Hour, 0.2, 0),
})

<span class="tok-c">// Schedule a periodic job with a 5-field cron expression.</span>
engine, _ = oban.New(oban.Config{
    Store:  oban.NewInMemoryStore(),
    Queues: map[string]int{"default": 5},
    Periodic: []oban.Periodic{{
        Schedule: oban.MustParseCron("0 * * * *"), <span class="tok-c">// top of every hour</span>
        Worker:   "digest",
        Args:     map[string]string{"kind": "hourly"},
    }},
})

<span class="tok-c">// De-duplicate: skip an enqueue if an unfinished job with the same</span>
<span class="tok-c">// queue + worker + key was inserted within the window.</span>
job, _ := oban.NewJob("email", map[string]string{"to": "ada@example.com"},
    oban.WithUnique("ada@example.com", time.Minute),
    oban.WithPriority(0))
_, dup, _ := engine.Enqueue(ctx, job)

<span class="tok-c">// Graceful shutdown: stop fetching and drain in-flight jobs.</span>
shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
_ = engine.Stop(shutdown)`
};
