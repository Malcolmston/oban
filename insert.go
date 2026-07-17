package oban

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// UniqueField names one dimension over which a unique insert de-duplicates.
// A [UniqueBy] selects a set of fields; two jobs collide only when they agree
// on every selected field (in addition to any explicit [UniqueBy.Keys]).
type UniqueField string

const (
	// UniqueWorker de-duplicates on the job's Worker.
	UniqueWorker UniqueField = "worker"
	// UniqueQueue de-duplicates on the job's Queue.
	UniqueQueue UniqueField = "queue"
	// UniqueArgs de-duplicates on the job's (canonicalized) Args JSON.
	UniqueArgs UniqueField = "args"
	// UniquePriority de-duplicates on the job's Priority.
	UniquePriority UniqueField = "priority"
)

// UniqueBy describes Elixir-Oban-style multi-dimension unique-job semantics for
// a single insert. It generalizes the single-key [WithUnique] option: a job is
// considered a duplicate of an existing one when they match on every selected
// Field and every explicit Key, the existing job is in one of States, and it was
// inserted within Period of the new job.
//
// The zero value is not meaningful; construct a UniqueBy with at least a Period.
// Unset States default to the unfinished set (available, scheduled, executing,
// retryable) and unset Fields default to worker+queue+args, matching Elixir Oban.
type UniqueBy struct {
	// Period is the window over which duplicates are detected, measured against
	// the new job's insertion time. A non-positive Period disables uniqueness.
	Period time.Duration
	// States restricts which existing job states can block the insert. When nil
	// it defaults to the unfinished set.
	States []State
	// Fields selects the job dimensions that must match. When nil it defaults to
	// worker+queue+args.
	Fields []UniqueField
	// Keys are additional opaque strings that must match for a collision. They
	// are combined with the selected Fields to form the effective unique key.
	Keys []string
}

// insertWithDefaults returns a copy of by with States and Fields filled in from
// the Elixir-Oban defaults when the caller left them unset.
func (by UniqueBy) insertWithDefaults() UniqueBy {
	out := by
	if out.States == nil {
		out.States = []State{
			StateAvailable,
			StateScheduled,
			StateExecuting,
			StateRetryable,
		}
	}
	if out.Fields == nil {
		out.Fields = []UniqueField{UniqueWorker, UniqueQueue, UniqueArgs}
	}
	return out
}

// ReplaceField names a job field that a unique insert may overwrite on the
// existing (conflicting) job instead of skipping the insert entirely.
type ReplaceField string

const (
	// ReplaceScheduledAt overwrites the conflict's ScheduledAt.
	ReplaceScheduledAt ReplaceField = "scheduled_at"
	// ReplacePriority overwrites the conflict's Priority.
	ReplacePriority ReplaceField = "priority"
	// ReplaceMaxAttempts overwrites the conflict's MaxAttempts.
	ReplaceMaxAttempts ReplaceField = "max_attempts"
	// ReplaceArgs overwrites the conflict's Args.
	ReplaceArgs ReplaceField = "args"
	// ReplaceTags overwrites the conflict's tags (persisted via TaggableStore).
	ReplaceTags ReplaceField = "tags"
	// ReplaceMeta overwrites the conflict's meta (persisted via TaggableStore).
	ReplaceMeta ReplaceField = "meta"
)

// InsertOpts carries the rich insert options layered over a plain [Store]
// enqueue: multi-dimension uniqueness, replace-on-conflict, and side-channel
// tags and metadata.
type InsertOpts struct {
	// Unique, when non-nil, enables unique-job de-duplication for the insert.
	Unique *UniqueBy
	// Replace lists the fields to overwrite on an existing conflict instead of
	// leaving it untouched. It is only consulted when Unique detects a conflict.
	Replace []ReplaceField
	// Tags are free-form labels persisted alongside the job via a TaggableStore.
	Tags []string
	// Meta is an arbitrary metadata map persisted alongside the job via a
	// TaggableStore.
	Meta map[string]any
}

// ValidatePriority reports whether p is an acceptable job priority. Like Elixir
// Oban, priorities are constrained to the inclusive range 0..9 (lower runs
// first). It returns a non-nil error for any value outside that range.
func ValidatePriority(p int) error {
	if p < 0 || p > 9 {
		return errors.New("oban: priority must be between 0 and 9")
	}
	return nil
}

// UniqueStore is the optional capability a [Store] implements to answer
// multi-dimension unique-insert lookups. Stores that cannot answer richer
// queries may omit it, in which case [Insert] falls back to the single-key
// unique support built into the plain [Store] contract.
type UniqueStore interface {
	// FindConflict returns an existing job that blocks the insert of job under
	// the resolved uniqueness rule by (matching over the chosen Fields and Keys
	// within Period across the given States, measured against now), or nil when
	// there is no conflict.
	FindConflict(ctx context.Context, job *Job, by UniqueBy, now time.Time) (*Job, error)
}

// TaggableStore is the optional capability a [Store] implements to persist and
// read back the tags and metadata that [Job] itself does not carry.
type TaggableStore interface {
	// SetTagsMeta replaces the stored tags and meta for the job with the given
	// id.
	SetTagsMeta(ctx context.Context, id int64, tags []string, meta map[string]any) error
	// TagsMeta returns the stored tags and meta for the job with the given id.
	TagsMeta(ctx context.Context, id int64) (tags []string, meta map[string]any, err error)
}

// Insert enqueues job with Elixir-Oban-style rich semantics on top of the plain
// [Store] contract.
//
// It first validates job.Priority with [ValidatePriority]. When opts.Unique is
// set and store implements [UniqueStore], it asks the store for a conflict; on a
// hit it either returns the existing job unchanged (inserted=false) or, when
// opts.Replace is non-empty, overwrites the listed fields on that conflict and
// returns it (still inserted=false). When the store does not implement
// [UniqueStore], a single-dimension unique request is mapped onto
// job.UniqueKey/UniquePeriod so the built-in de-duplication still applies.
//
// With no conflict the call delegates to store.Enqueue. On a freshly inserted
// job, opts.Tags and opts.Meta are persisted through [TaggableStore] when the
// store supports it; stores without that capability simply ignore them.
func Insert(ctx context.Context, store Store, job *Job, opts InsertOpts) (result *Job, inserted bool, err error) {
	if job == nil {
		return nil, false, errors.New("oban: cannot insert nil job")
	}
	if err := ValidatePriority(job.Priority); err != nil {
		return nil, false, err
	}

	now := job.InsertedAt
	if now.IsZero() {
		now = time.Now()
	}

	if opts.Unique != nil && opts.Unique.Period > 0 {
		by := opts.Unique.insertWithDefaults()
		if us, ok := store.(UniqueStore); ok {
			conflict, ferr := us.FindConflict(ctx, job, by, now)
			if ferr != nil {
				return nil, false, ferr
			}
			if conflict != nil {
				if len(opts.Replace) > 0 {
					if aerr := insertApplyReplace(ctx, store, conflict, job, opts); aerr != nil {
						return nil, false, aerr
					}
				}
				return conflict, false, nil
			}
		} else {
			// No rich unique support: fold the request down onto the built-in
			// single-key de-duplication so an InMemoryStore still de-dupes.
			job.UniqueKey = insertUniqueKey(job, by)
			job.UniquePeriod = by.Period
		}
	}

	result, inserted, err = store.Enqueue(ctx, job)
	if err != nil {
		return nil, false, err
	}

	if inserted && (len(opts.Tags) > 0 || len(opts.Meta) > 0) {
		if ts, ok := store.(TaggableStore); ok {
			if serr := ts.SetTagsMeta(ctx, result.ID, opts.Tags, opts.Meta); serr != nil {
				return nil, false, serr
			}
		}
	}

	return result, inserted, nil
}

// Tags returns the tags stored for the job with the given id. It reads them back
// through [TaggableStore]; when store does not implement that capability it
// returns an empty slice and a nil error.
func Tags(ctx context.Context, store Store, id int64) ([]string, error) {
	ts, ok := store.(TaggableStore)
	if !ok {
		return nil, nil
	}
	tags, _, err := ts.TagsMeta(ctx, id)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// insertApplyReplace overwrites the fields listed in opts.Replace on the conflict
// job using the values from the incoming job (and opts for tags/meta), persisting
// tags and meta through a [TaggableStore] when one is available.
func insertApplyReplace(ctx context.Context, store Store, conflict, incoming *Job, opts InsertOpts) error {
	var replaceTags, replaceMeta bool
	for _, f := range opts.Replace {
		switch f {
		case ReplaceScheduledAt:
			conflict.ScheduledAt = incoming.ScheduledAt
		case ReplacePriority:
			conflict.Priority = incoming.Priority
		case ReplaceMaxAttempts:
			conflict.MaxAttempts = incoming.MaxAttempts
		case ReplaceArgs:
			conflict.Args = append(json.RawMessage(nil), incoming.Args...)
		case ReplaceTags:
			replaceTags = true
		case ReplaceMeta:
			replaceMeta = true
		}
	}
	if !replaceTags && !replaceMeta {
		return nil
	}
	ts, ok := store.(TaggableStore)
	if !ok {
		return nil
	}
	tags, meta, err := ts.TagsMeta(ctx, conflict.ID)
	if err != nil {
		return err
	}
	if replaceTags {
		tags = opts.Tags
	}
	if replaceMeta {
		meta = opts.Meta
	}
	return ts.SetTagsMeta(ctx, conflict.ID, tags, meta)
}

// insertUniqueKey derives a deterministic single-string unique key from the
// resolved uniqueness rule so that stores without [UniqueStore] support can
// de-duplicate through the built-in job.UniqueKey mechanism. The key encodes the
// explicit Keys plus the value of each selected field, in a stable order.
func insertUniqueKey(job *Job, by UniqueBy) string {
	parts := make([]string, 0, len(by.Keys)+len(by.Fields))
	parts = append(parts, by.Keys...)
	for _, f := range by.Fields {
		switch f {
		case UniqueWorker:
			parts = append(parts, "worker="+job.Worker)
		case UniqueQueue:
			parts = append(parts, "queue="+job.Queue)
		case UniqueArgs:
			parts = append(parts, "args="+insertCanonicalArgs(job.Args))
		case UniquePriority:
			pb, _ := json.Marshal(job.Priority)
			parts = append(parts, "priority="+string(pb))
		}
	}
	sort.Strings(parts)
	b, _ := json.Marshal(parts)
	return string(b)
}

// insertCanonicalArgs returns a canonical JSON encoding of raw (object keys
// sorted by encoding/json) so that logically equal argument maps produce the
// same unique key regardless of source key order. It falls back to the raw bytes
// when raw is not valid JSON.
func insertCanonicalArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}
