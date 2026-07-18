package oban

import (
	"context"
	"errors"
	"fmt"
)

// InsertResult pairs one job of a batch insert with whether it was newly
// inserted, so callers can tell freshly enqueued jobs apart from ones that were
// de-duplicated against an existing unique job.
type InsertResult struct {
	// Job is the stored job (freshly inserted, or the existing conflict when
	// Inserted is false).
	Job *Job
	// Inserted reports whether a new row was created for this job.
	Inserted bool
}

// InsertMany enqueues each job in jobs into store in order, returning a
// per-job [InsertResult]. It is the batch companion to a single
// [Store.Enqueue], mirroring Elixir Oban's insert_all: every job is inserted
// with the store's own unique-job semantics honored independently.
//
// Insertion stops at the first error, returning the results accumulated so far
// alongside the error. A nil entry in jobs is an error. Each job's InsertedAt,
// when unset, is left for the store to stamp.
func InsertMany(ctx context.Context, store Store, jobs []*Job) ([]InsertResult, error) {
	results := make([]InsertResult, 0, len(jobs))
	for i, job := range jobs {
		if job == nil {
			return results, errors.New("oban: cannot insert nil job in batch")
		}
		stored, inserted, err := store.Enqueue(ctx, job)
		if err != nil {
			return results, fmt.Errorf("oban: insert many: job %d: %w", i, err)
		}
		results = append(results, InsertResult{Job: stored, Inserted: inserted})
	}
	return results, nil
}

// EnqueueMany inserts each job in jobs through the engine in order, stamping any
// unset insertion time from the engine clock exactly as [Oban.Enqueue] does. It
// returns a per-job [InsertResult] and stops at the first error, returning the
// results gathered so far. It is the batch companion to [Oban.Enqueue].
func (o *Oban) EnqueueMany(ctx context.Context, jobs []*Job) ([]InsertResult, error) {
	results := make([]InsertResult, 0, len(jobs))
	for i, job := range jobs {
		if job == nil {
			return results, errors.New("oban: cannot enqueue nil job in batch")
		}
		stored, inserted, err := o.Enqueue(ctx, job)
		if err != nil {
			return results, fmt.Errorf("oban: enqueue many: job %d: %w", i, err)
		}
		results = append(results, InsertResult{Job: stored, Inserted: inserted})
	}
	return results, nil
}
