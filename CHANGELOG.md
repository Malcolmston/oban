# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/), and the project aims
to follow semantic versioning.

## [0.3.0]

Parity-focused expansion of the public API, standard library only. All additions
ship with deterministic known-answer tests and, where relevant, benchmarks.

### Added

- **InMemoryStore now implements every optional store capability**, closing the
  largest parity gap: the in-memory store previously supported none of them, so
  the control plane, pruner, lifeline, snooze/cancel signals and rich unique
  inserts only worked against a SQL-backed store. It now satisfies
  `CancelableStore`, `RetryableStore`, `DeletableStore`, `SnoozableStore`,
  `PrunableStore`, `RescuableStore`, `UniqueStore` and `TaggableStore` via new
  methods `Cancel`, `Delete`, `RetryNow`, `Snooze`, `DeleteFinishedBefore`,
  `RescueExecuting`, `FindConflict`, `SetTagsMeta` and `TagsMeta` (compile-time
  asserted against each interface).
- **Job listing and filtering**: `JobFilter` (with `Matches`), the
  `ListableStore` capability, `InMemoryStore.List`, and the package helpers
  `ListJobs` and `CountJobs`.
- **Testing helpers** mirroring Elixir Oban's `Oban.Testing`: `RunJob`,
  `PerformJob`, `AssertEnqueued`, `RefuteEnqueued`, and the `PerformResult` /
  `PerformOutcome` classification of a worker's return value (including the
  snooze and cancel signals).
- **Additional backoff strategies**: `ConstantBackoff`, `LinearBackoff`,
  `FibonacciBackoff` and the `BackoffFunc` adapter, each with a constructor.
- **Cron helpers**: `ParseCronSpec` / `MustParseCronSpec` accept the named
  shorthands (`@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`,
  `@midnight`, `@annually`), plus `Schedule.Matches`, `Schedule.Prev` and
  `Schedule.Upcoming`.
- **Batch inserts**: `InsertMany`, `Oban.EnqueueMany` and the `InsertResult`
  type, honoring per-job unique semantics.
- **Rate limiting**: a deterministic, clock-injectable token-bucket
  `RateLimiter` (`Allow`, `AllowN`, `Tokens`).
- **Job introspection** on `Job`: `IsFinished`, `InState`, `AttemptsRemaining`,
  `HasErrored`, `ExecutionDuration` and `Age`.
- **InMemoryStore introspection**: `Len`, `Clear`, `CountByQueue` and `States`.

### Changed

- `InMemoryStore` gained internal storage for tags and meta (side-channel
  columns) to back the `TaggableStore` capability. Existing behavior is
  unchanged.
