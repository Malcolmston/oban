package oban

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLDialect identifies the SQL flavor an [SQLStore] targets. It selects the
// placeholder syntax, the row-locking clause used by FetchAvailable, and the
// strategy used to recover the auto-assigned primary key on insert.
type SQLDialect int

const (
	// Postgres targets PostgreSQL: $N placeholders, SELECT ... FOR UPDATE SKIP
	// LOCKED, and RETURNING id to recover the inserted primary key.
	Postgres SQLDialect = iota
	// SQLite targets SQLite: ? placeholders, no SKIP LOCKED (writes are
	// serialized by the database), and LastInsertId to recover the primary key.
	SQLite
	// MySQL targets MySQL/MariaDB: ? placeholders, SELECT ... FOR UPDATE SKIP
	// LOCKED, and LastInsertId to recover the primary key.
	MySQL
)

// String returns the lowercase name of the dialect ("postgres", "sqlite" or
// "mysql"), or "unknown" for an unrecognized value.
func (d SQLDialect) String() string {
	switch d {
	case Postgres:
		return "postgres"
	case SQLite:
		return "sqlite"
	case MySQL:
		return "mysql"
	default:
		return "unknown"
	}
}

// DefaultSQLTable is the table name an [SQLStore] uses when [WithTableName] is
// not supplied.
const DefaultSQLTable = "oban_jobs"

// SQLStore is a durable [Store] backed by the standard database/sql package. It
// works against any driver the caller wires up (the driver is never imported
// here) and speaks one of the dialects enumerated by [SQLDialect].
//
// SQLStore satisfies [Store] and, in addition, the optional capability
// interfaces the plugins, control and insert areas type-assert for: pruning,
// rescuing, cancelling, deleting, forcing a retry, unique-conflict lookup and
// tag/meta updates. All operations are plain SQL executed with encoding/json for
// the JSON-typed columns.
//
// A zero SQLStore is not usable; construct one with [NewSQLStore]. SQLStore is
// safe for concurrent use to the extent the underlying *sql.DB (a connection
// pool) is.
type SQLStore struct {
	db      *sql.DB
	dialect SQLDialect
	table   string
}

// SQLOption customizes an [SQLStore] built with [NewSQLStore].
type SQLOption func(*SQLStore)

// WithTableName overrides the table an [SQLStore] reads and writes. An empty
// name is ignored and the default ([DefaultSQLTable]) is kept.
func WithTableName(name string) SQLOption {
	return func(s *SQLStore) {
		if name != "" {
			s.table = name
		}
	}
}

// NewSQLStore returns an [SQLStore] that persists jobs in db using the given
// dialect. Options are applied in order. The database schema is not created
// automatically; call [SQLStore.Migrate] once before use.
func NewSQLStore(db *sql.DB, dialect SQLDialect, opts ...SQLOption) *SQLStore {
	s := &SQLStore{
		db:      db,
		dialect: dialect,
		table:   DefaultSQLTable,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Compile-time assertion that SQLStore satisfies the core Store contract.
var _ Store = (*SQLStore)(nil)

// sqlstoreSelectColumns is the column list, in Job/scan order, read by every
// SELECT. It mirrors the exported fields of [Job] plus the tags and meta side
// columns.
const sqlstoreSelectColumns = "id, queue, worker, args, max_attempts, attempt, " +
	"priority, state, scheduled_at, inserted_at, attempted_at, completed_at, " +
	"discarded_at, unique_key, unique_period_ns, last_error, errors, tags, meta"

// sqlstoreInsertColumns is the column list, in bind order, written by Enqueue.
// It is sqlstoreSelectColumns without the auto-assigned id.
const sqlstoreInsertColumns = "queue, worker, args, max_attempts, attempt, " +
	"priority, state, scheduled_at, inserted_at, attempted_at, completed_at, " +
	"discarded_at, unique_key, unique_period_ns, last_error, errors, tags, meta"

// sqlstoreFetchableStates lists, in deterministic order, the states from which a
// job may be fetched for execution. It matches the package-level fetchableStates
// set but is ordered for use in SQL IN clauses.
var sqlstoreFetchableStates = []State{StateAvailable, StateScheduled, StateRetryable}

// sqlstoreUnfinishedStates lists, in deterministic order, the states that block a
// unique-job insert. It matches the package-level unfinishedStates set.
var sqlstoreUnfinishedStates = []State{StateAvailable, StateScheduled, StateExecuting, StateRetryable}

// sqlstoreFinishedStates lists the terminal states pruning considers deletable.
var sqlstoreFinishedStates = []State{StateCompleted, StateDiscarded, StateCancelled}

// sqlstoreQueryer is satisfied by both *sql.DB and *sql.Tx, letting the
// conflict-lookup helper run either standalone or inside a transaction.
type sqlstoreQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// sqlstoreScanner is the common surface of *sql.Row and *sql.Rows.
type sqlstoreScanner interface {
	Scan(dest ...any) error
}

// Migrate creates the jobs table and its indexes if they do not already exist.
// It issues CREATE TABLE IF NOT EXISTS with a column for every exported [Job]
// field plus tags and meta JSON side columns, and two indexes: one on
// (queue, state, priority, scheduled_at, id) for fetching and one on
// (state, worker, unique_key) for uniqueness lookups. Migrate is idempotent.
func (s *SQLStore) Migrate(ctx context.Context) error {
	for _, stmt := range s.sqlstoreDDL() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("oban: migrate: %w", err)
		}
	}
	return nil
}

// sqlstoreDDL returns the ordered DDL statements that build the schema for the
// configured dialect and table.
func (s *SQLStore) sqlstoreDDL() []string {
	var idType, textType, keyType, stateType, jsonType, tsType string
	switch s.dialect {
	case Postgres:
		idType, textType, keyType, stateType, jsonType, tsType = "BIGSERIAL PRIMARY KEY", "TEXT", "TEXT", "TEXT", "JSONB", "TIMESTAMPTZ"
	case SQLite:
		idType, textType, keyType, stateType, jsonType, tsType = "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT", "TEXT", "TEXT", "DATETIME"
	case MySQL:
		idType, textType, keyType, stateType, jsonType, tsType = "BIGINT AUTO_INCREMENT PRIMARY KEY", "TEXT", "VARCHAR(255)", "VARCHAR(64)", "JSON", "DATETIME(6)"
	default:
		idType, textType, keyType, stateType, jsonType, tsType = "BIGSERIAL PRIMARY KEY", "TEXT", "TEXT", "TEXT", "JSONB", "TIMESTAMPTZ"
	}

	createTable := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s (
	id %[2]s,
	queue %[3]s NOT NULL,
	worker %[3]s NOT NULL,
	args %[4]s NOT NULL,
	max_attempts INTEGER NOT NULL,
	attempt INTEGER NOT NULL,
	priority INTEGER NOT NULL,
	state %[5]s NOT NULL,
	scheduled_at %[6]s NOT NULL,
	inserted_at %[6]s NOT NULL,
	attempted_at %[6]s,
	completed_at %[6]s,
	discarded_at %[6]s,
	unique_key %[3]s,
	unique_period_ns BIGINT NOT NULL,
	last_error %[7]s,
	errors %[4]s NOT NULL,
	tags %[4]s NOT NULL,
	meta %[4]s NOT NULL
)`, s.table, idType, keyType, jsonType, stateType, tsType, textType)

	// MySQL does not accept IF NOT EXISTS on CREATE INDEX; the others do.
	ine := "IF NOT EXISTS "
	if s.dialect == MySQL {
		ine = ""
	}
	fetchIdx := fmt.Sprintf("CREATE INDEX %s%s ON %s (queue, state, priority, scheduled_at, id)",
		ine, s.sqlstoreIndexName("fetch"), s.table)
	uniqueIdx := fmt.Sprintf("CREATE INDEX %s%s ON %s (state, worker, unique_key)",
		ine, s.sqlstoreIndexName("unique"), s.table)

	return []string{createTable, fetchIdx, uniqueIdx}
}

// sqlstoreIndexName derives a stable index name from the table name and a
// suffix, replacing any non-alphanumeric character so schema-qualified table
// names still yield a valid identifier.
func (s *SQLStore) sqlstoreIndexName(suffix string) string {
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, s.table)
	return "idx_" + base + "_" + suffix
}

// sqlstoreRebind rewrites a query written with ? placeholders into the
// dialect's placeholder syntax. Postgres uses positional $N markers; SQLite and
// MySQL keep ?. Queries here contain no literal question marks, so a plain
// left-to-right substitution is safe.
func (s *SQLStore) sqlstoreRebind(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// itoa renders a small non-negative int without pulling in strconv at the call
// sites; it is used only for placeholder numbering.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// sqlstoreStateArgs turns a slice of States into a comma-separated run of
// placeholders and the matching argument slice for an IN clause.
func sqlstoreStateArgs(states []State) (string, []any) {
	marks := make([]string, len(states))
	args := make([]any, len(states))
	for i, st := range states {
		marks[i] = "?"
		args[i] = string(st)
	}
	return strings.Join(marks, ", "), args
}

// sqlstoreForUpdate returns the row-locking clause used by FetchAvailable for
// the dialect: FOR UPDATE SKIP LOCKED where supported, empty on SQLite.
func (s *SQLStore) sqlstoreForUpdate() string {
	switch s.dialect {
	case Postgres, MySQL:
		return " FOR UPDATE SKIP LOCKED"
	default:
		return ""
	}
}

// sqlstoreLockRow returns a plain FOR UPDATE clause (no SKIP LOCKED) used when a
// single row is read for a guarded update inside a transaction.
func (s *SQLStore) sqlstoreLockRow() string {
	switch s.dialect {
	case Postgres, MySQL:
		return " FOR UPDATE"
	default:
		return ""
	}
}

// Enqueue implements [Store]. It resolves any relative schedule against the
// insert time, normalizes the state to StateScheduled or StateAvailable, honors
// unique-job de-duplication atomically within a transaction, and assigns the id
// via RETURNING (Postgres) or LastInsertId (SQLite, MySQL).
func (s *SQLStore) Enqueue(ctx context.Context, job *Job) (*Job, bool, error) {
	if job == nil {
		return nil, false, errors.New("oban: cannot enqueue nil job")
	}

	now := job.InsertedAt
	if now.IsZero() {
		now = time.Now()
	}

	stored := job.Clone()
	stored.InsertedAt = now
	if job.hasScheduleIn {
		stored.ScheduledAt = now.Add(job.scheduleIn)
	}
	if stored.ScheduledAt.IsZero() {
		stored.ScheduledAt = now
	}
	if stored.ScheduledAt.After(now) {
		stored.State = StateScheduled
	} else {
		stored.State = StateAvailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("oban: enqueue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if stored.UniqueKey != "" && stored.UniquePeriod > 0 {
		existing, err := s.sqlstoreFindConflict(ctx, tx, stored, now)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			if err := tx.Commit(); err != nil {
				return nil, false, fmt.Errorf("oban: enqueue: commit: %w", err)
			}
			return existing, false, nil
		}
	}

	id, err := s.sqlstoreInsert(ctx, tx, stored)
	if err != nil {
		return nil, false, err
	}
	stored.ID = id

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("oban: enqueue: commit: %w", err)
	}
	return stored.Clone(), true, nil
}

// sqlstoreInsert writes j within tx and returns the assigned id.
func (s *SQLStore) sqlstoreInsert(ctx context.Context, tx *sql.Tx, j *Job) (int64, error) {
	errsJSON, err := sqlstoreMarshalErrors(j.Errors)
	if err != nil {
		return 0, err
	}
	args := j.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	values := []any{
		j.Queue,
		j.Worker,
		[]byte(args),
		j.MaxAttempts,
		j.Attempt,
		j.Priority,
		string(j.State),
		j.ScheduledAt,
		j.InsertedAt,
		sqlstoreNullTime(j.AttemptedAt),
		sqlstoreNullTime(j.CompletedAt),
		sqlstoreNullTime(j.DiscardedAt),
		sqlstoreNullStr(j.UniqueKey),
		int64(j.UniquePeriod),
		sqlstoreNullStr(j.LastError),
		errsJSON,
		[]byte("[]"),
		[]byte("{}"),
	}

	marks := make([]string, len(values))
	for i := range marks {
		marks[i] = "?"
	}
	base := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		s.table, sqlstoreInsertColumns, strings.Join(marks, ", "))

	if s.dialect == Postgres {
		var id int64
		q := s.sqlstoreRebind(base + " RETURNING id")
		if err := tx.QueryRowContext(ctx, q, values...).Scan(&id); err != nil {
			return 0, fmt.Errorf("oban: insert: %w", err)
		}
		return id, nil
	}

	res, err := tx.ExecContext(ctx, s.sqlstoreRebind(base), values...)
	if err != nil {
		return 0, fmt.Errorf("oban: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("oban: insert: last insert id: %w", err)
	}
	return id, nil
}

// sqlstoreFindConflict returns an unfinished job that conflicts with j on
// (queue, worker, unique_key) and was inserted within j.UniquePeriod, or nil if
// none exists. It runs against any sqlstoreQueryer (db or tx).
func (s *SQLStore) sqlstoreFindConflict(ctx context.Context, q sqlstoreQueryer, j *Job, now time.Time) (*Job, error) {
	marks, stateArgs := sqlstoreStateArgs(sqlstoreUnfinishedStates)
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE queue = ? AND worker = ? AND unique_key = ? "+
			"AND state IN (%s) AND inserted_at >= ? ORDER BY id ASC LIMIT 1",
		sqlstoreSelectColumns, s.table, marks)

	args := make([]any, 0, 4+len(stateArgs))
	args = append(args, j.Queue, j.Worker, j.UniqueKey)
	args = append(args, stateArgs...)
	args = append(args, now.Add(-j.UniquePeriod))

	row := q.QueryRowContext(ctx, s.sqlstoreRebind(query), args...)
	job, err := sqlstoreScanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("oban: find conflict: %w", err)
	}
	return job, nil
}

// FetchAvailable implements [Store]. In a single transaction it selects the
// fetchable rows for queue whose scheduled_at has passed, ordered by priority,
// scheduled_at then id, locking them with FOR UPDATE SKIP LOCKED on Postgres and
// MySQL (and relying on SQLite's serialized writes otherwise). Each selected job
// is transitioned to StateExecuting with its attempt incremented and
// attempted_at set, and returned as a clone.
func (s *SQLStore) FetchAvailable(ctx context.Context, queue string, limit int, now time.Time) ([]*Job, error) {
	if limit <= 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("oban: fetch: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	marks, stateArgs := sqlstoreStateArgs(sqlstoreFetchableStates)
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE queue = ? AND state IN (%s) AND scheduled_at <= ? "+
			"ORDER BY priority ASC, scheduled_at ASC, id ASC LIMIT ?%s",
		sqlstoreSelectColumns, s.table, marks, s.sqlstoreForUpdate())

	args := make([]any, 0, 3+len(stateArgs))
	args = append(args, queue)
	args = append(args, stateArgs...)
	args = append(args, now, limit)

	rows, err := tx.QueryContext(ctx, s.sqlstoreRebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("oban: fetch: select: %w", err)
	}
	var jobs []*Job
	for rows.Next() {
		job, err := sqlstoreScanJob(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("oban: fetch: scan: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("oban: fetch: rows: %w", err)
	}
	_ = rows.Close()

	update := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET state = ?, attempt = attempt + 1, attempted_at = ? WHERE id = ?",
		s.table))
	out := make([]*Job, 0, len(jobs))
	for _, job := range jobs {
		if _, err := tx.ExecContext(ctx, update, string(StateExecuting), now, job.ID); err != nil {
			return nil, fmt.Errorf("oban: fetch: transition: %w", err)
		}
		job.State = StateExecuting
		job.Attempt++
		job.AttemptedAt = now
		out = append(out, job.Clone())
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("oban: fetch: commit: %w", err)
	}
	return out, nil
}

// Complete implements [Store]. It moves the job to StateCompleted only if it is
// still executing; a job that has already moved on (for example cancelled or
// rescued) is left untouched, which is what makes operator overrides stick.
func (s *SQLStore) Complete(ctx context.Context, id int64, now time.Time) error {
	q := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET state = ?, completed_at = ? WHERE id = ? AND state = ?", s.table))
	if _, err := s.db.ExecContext(ctx, q, string(StateCompleted), now, id, string(StateExecuting)); err != nil {
		return fmt.Errorf("oban: complete: %w", err)
	}
	return nil
}

// Retry implements [Store]. It records lastErr in the errors JSON, moves the job
// to StateRetryable and sets scheduled_at, but only while the job is still
// executing; otherwise it is a no-op.
func (s *SQLStore) Retry(ctx context.Context, id int64, scheduledAt time.Time, lastErr error, now time.Time) error {
	return s.sqlstoreFail(ctx, id, StateRetryable, "scheduled_at", scheduledAt, lastErr, now)
}

// Discard implements [Store]. It records lastErr in the errors JSON and moves
// the job to StateDiscarded with discarded_at set, but only while the job is
// still executing; otherwise it is a no-op.
func (s *SQLStore) Discard(ctx context.Context, id int64, lastErr error, now time.Time) error {
	return s.sqlstoreFail(ctx, id, StateDiscarded, "discarded_at", now, lastErr, now)
}

// sqlstoreFail performs the read-modify-write shared by Retry and Discard:
// append the failed attempt to the errors history and transition the job,
// stamping tsColumn with tsValue, guarded by state = executing.
func (s *SQLStore) sqlstoreFail(ctx context.Context, id int64, newState State, tsColumn string, tsValue time.Time, lastErr error, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oban: %s: begin: %w", newState, err)
	}
	defer func() { _ = tx.Rollback() }()

	sel := s.sqlstoreRebind(fmt.Sprintf(
		"SELECT errors, attempt FROM %s WHERE id = ? AND state = ?%s",
		s.table, s.sqlstoreLockRow()))
	var errsJSON []byte
	var attempt int
	err = tx.QueryRowContext(ctx, sel, id, string(StateExecuting)).Scan(&errsJSON, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		// Job already moved on: no-op, keep transaction consistent.
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("oban: %s: select: %w", newState, err)
	}

	history, err := sqlstoreUnmarshalErrors(errsJSON)
	if err != nil {
		return fmt.Errorf("oban: %s: %w", newState, err)
	}
	lastMsg := ""
	if lastErr != nil {
		lastMsg = lastErr.Error()
		history = append(history, AttemptError{Attempt: attempt, At: now, Message: lastMsg})
	}
	newErrs, err := sqlstoreMarshalErrors(history)
	if err != nil {
		return fmt.Errorf("oban: %s: %w", newState, err)
	}

	upd := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET state = ?, %s = ?, last_error = ?, errors = ? WHERE id = ? AND state = ?",
		s.table, tsColumn))
	if _, err := tx.ExecContext(ctx, upd, string(newState), tsValue, sqlstoreNullStr(lastMsg), newErrs, id, string(StateExecuting)); err != nil {
		return fmt.Errorf("oban: %s: update: %w", newState, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("oban: %s: commit: %w", newState, err)
	}
	return nil
}

// Get implements [Store]. It returns a copy of the job with the given id, or
// [ErrJobNotFound].
func (s *SQLStore) Get(ctx context.Context, id int64) (*Job, error) {
	q := s.sqlstoreRebind(fmt.Sprintf(
		"SELECT %s FROM %s WHERE id = ?", sqlstoreSelectColumns, s.table))
	job, err := sqlstoreScanJob(s.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oban: get: %w", err)
	}
	return job, nil
}

// DeleteFinishedBefore deletes every job in a terminal state (completed,
// discarded or cancelled) whose finishing time is strictly before cutoff, and
// returns the number of rows removed. It implements the pruning capability the
// plugins area relies on.
func (s *SQLStore) DeleteFinishedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	marks, stateArgs := sqlstoreStateArgs(sqlstoreFinishedStates)
	q := s.sqlstoreRebind(fmt.Sprintf(
		"DELETE FROM %s WHERE state IN (%s) AND "+
			"COALESCE(completed_at, discarded_at, inserted_at) < ?",
		s.table, marks))
	args := append(append([]any{}, stateArgs...), cutoff)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("oban: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("oban: prune: rows affected: %w", err)
	}
	return n, nil
}

// RescueExecuting recovers jobs stuck in StateExecuting whose attempted_at is
// before olderThan (their runner most likely crashed). Jobs with attempts to
// spare are returned to StateAvailable scheduled at now; jobs that have
// exhausted their attempts are discarded with discarded_at set to now. It
// returns the number of jobs rescued and implements the rescue capability the
// plugins area relies on.
func (s *SQLStore) RescueExecuting(ctx context.Context, olderThan time.Time, now time.Time) (int64, error) {
	q := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET "+
			"state = CASE WHEN attempt < max_attempts THEN ? ELSE ? END, "+
			"scheduled_at = CASE WHEN attempt < max_attempts THEN ? ELSE scheduled_at END, "+
			"discarded_at = CASE WHEN attempt < max_attempts THEN discarded_at ELSE ? END "+
			"WHERE state = ? AND attempted_at < ?",
		s.table))
	res, err := s.db.ExecContext(ctx, q,
		string(StateAvailable), string(StateDiscarded),
		now, now,
		string(StateExecuting), olderThan)
	if err != nil {
		return 0, fmt.Errorf("oban: rescue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("oban: rescue: rows affected: %w", err)
	}
	return n, nil
}

// Cancel moves a job to StateCancelled unless it is already in a terminal state
// (completed, discarded or cancelled), in which case it is a no-op. Cancelling a
// job that is executing is permitted: the guard on Complete/Retry/Discard then
// prevents the finishing runner from overwriting the cancellation. It implements
// the cancel capability the control area relies on.
func (s *SQLStore) Cancel(ctx context.Context, id int64, now time.Time) error {
	marks, stateArgs := sqlstoreStateArgs(sqlstoreFinishedStates)
	q := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET state = ?, discarded_at = ? WHERE id = ? AND state NOT IN (%s)",
		s.table, marks))
	args := append([]any{string(StateCancelled), now, id}, stateArgs...)
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("oban: cancel: %w", err)
	}
	return nil
}

// Delete removes the job with the given id from the store. Deleting a job that
// does not exist is a no-op. It implements the delete capability the control
// area relies on.
func (s *SQLStore) Delete(ctx context.Context, id int64) error {
	q := s.sqlstoreRebind(fmt.Sprintf("DELETE FROM %s WHERE id = ?", s.table))
	if _, err := s.db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("oban: delete: %w", err)
	}
	return nil
}

// RetryNow makes a job runnable again immediately: it moves the job to
// StateAvailable, schedules it at now and clears completed_at and discarded_at.
// It is intended for retrying a discarded or cancelled job on demand and
// implements the retry capability the control area relies on.
func (s *SQLStore) RetryNow(ctx context.Context, id int64, now time.Time) error {
	q := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET state = ?, scheduled_at = ?, completed_at = NULL, discarded_at = NULL WHERE id = ?",
		s.table))
	if _, err := s.db.ExecContext(ctx, q, string(StateAvailable), now, id); err != nil {
		return fmt.Errorf("oban: retry now: %w", err)
	}
	return nil
}

// FindConflict returns the unfinished job that would block a unique insert of
// job (matching queue, worker and unique_key within job.UniquePeriod, measured
// against now), or nil if there is no conflict. It implements the unique-lookup
// capability the insert area relies on. It returns nil, nil when job carries no
// unique key or period.
func (s *SQLStore) FindConflict(ctx context.Context, job *Job, now time.Time) (*Job, error) {
	if job == nil || job.UniqueKey == "" || job.UniquePeriod <= 0 {
		return nil, nil
	}
	return s.sqlstoreFindConflict(ctx, s.db, job, now)
}

// SetTagsMeta replaces the tags and meta side columns of the job with the given
// id. tags is stored as a JSON array and meta as a JSON object; a nil tags or
// meta is stored as an empty array or object respectively. It implements the
// tagging capability the insert area relies on.
func (s *SQLStore) SetTagsMeta(ctx context.Context, id int64, tags []string, meta map[string]any) error {
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("oban: set tags/meta: marshal tags: %w", err)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("oban: set tags/meta: marshal meta: %w", err)
	}
	q := s.sqlstoreRebind(fmt.Sprintf(
		"UPDATE %s SET tags = ?, meta = ? WHERE id = ?", s.table))
	if _, err := s.db.ExecContext(ctx, q, tagsJSON, metaJSON, id); err != nil {
		return fmt.Errorf("oban: set tags/meta: %w", err)
	}
	return nil
}

// sqlstoreScanJob scans a single row (from *sql.Row or *sql.Rows) in
// sqlstoreSelectColumns order into a fresh *Job. The tags and meta side columns
// are read but discarded, as [Job] does not carry them.
func sqlstoreScanJob(sc sqlstoreScanner) (*Job, error) {
	var (
		j            Job
		args         []byte
		attemptedAt  sql.NullTime
		completedAt  sql.NullTime
		discardedAt  sql.NullTime
		uniqueKey    sql.NullString
		uniquePeriod int64
		lastError    sql.NullString
		errsJSON     []byte
		tagsJSON     []byte
		metaJSON     []byte
	)
	if err := sc.Scan(
		&j.ID,
		&j.Queue,
		&j.Worker,
		&args,
		&j.MaxAttempts,
		&j.Attempt,
		&j.Priority,
		&j.State,
		&j.ScheduledAt,
		&j.InsertedAt,
		&attemptedAt,
		&completedAt,
		&discardedAt,
		&uniqueKey,
		&uniquePeriod,
		&lastError,
		&errsJSON,
		&tagsJSON,
		&metaJSON,
	); err != nil {
		return nil, err
	}

	if len(args) > 0 {
		j.Args = append(json.RawMessage(nil), args...)
	}
	j.AttemptedAt = sqlstoreTime(attemptedAt)
	j.CompletedAt = sqlstoreTime(completedAt)
	j.DiscardedAt = sqlstoreTime(discardedAt)
	j.UniqueKey = uniqueKey.String
	j.UniquePeriod = time.Duration(uniquePeriod)
	j.LastError = lastError.String

	history, err := sqlstoreUnmarshalErrors(errsJSON)
	if err != nil {
		return nil, err
	}
	j.Errors = history

	return &j, nil
}

// sqlstoreMarshalErrors renders an attempt-error history as JSON, using an empty
// array (never null) for an empty history so the column stays valid JSON.
func sqlstoreMarshalErrors(errs []AttemptError) ([]byte, error) {
	if len(errs) == 0 {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(errs)
	if err != nil {
		return nil, fmt.Errorf("marshal errors: %w", err)
	}
	return b, nil
}

// sqlstoreUnmarshalErrors decodes an attempt-error history from JSON, treating
// empty or "null" input as no errors.
func sqlstoreUnmarshalErrors(b []byte) ([]AttemptError, error) {
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var out []AttemptError
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal errors: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// sqlstoreNullTime maps a time.Time to a sql.NullTime, treating the zero time as
// SQL NULL.
func sqlstoreNullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: !t.IsZero()}
}

// sqlstoreTime maps a sql.NullTime back to a time.Time, yielding the zero time
// for NULL.
func sqlstoreTime(n sql.NullTime) time.Time {
	if n.Valid {
		return n.Time
	}
	return time.Time{}
}

// sqlstoreNullStr maps a string to a sql.NullString, treating the empty string
// as SQL NULL.
func sqlstoreNullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
