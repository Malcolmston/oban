package oban

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLDialectString(t *testing.T) {
	tests := []struct {
		name    string
		dialect SQLDialect
		want    string
	}{
		{"postgres", Postgres, "postgres"},
		{"sqlite", SQLite, "sqlite"},
		{"mysql", MySQL, "mysql"},
		{"unknown", SQLDialect(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dialect.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewSQLStoreDefaults(t *testing.T) {
	tests := []struct {
		name string
		opts []SQLOption
		want string
	}{
		{"default table", nil, DefaultSQLTable},
		{"custom table", []SQLOption{WithTableName("jobs")}, "jobs"},
		{"empty name ignored", []SQLOption{WithTableName("")}, DefaultSQLTable},
		{"last wins", []SQLOption{WithTableName("a"), WithTableName("b")}, "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLStore((*sql.DB)(nil), Postgres, tt.opts...)
			if s.table != tt.want {
				t.Fatalf("table = %q, want %q", s.table, tt.want)
			}
			if s.dialect != Postgres {
				t.Fatalf("dialect = %v, want Postgres", s.dialect)
			}
		})
	}
}

func TestSQLStoreRebind(t *testing.T) {
	tests := []struct {
		name    string
		dialect SQLDialect
		query   string
		want    string
	}{
		{"postgres numbers placeholders", Postgres, "a = ? AND b = ?", "a = $1 AND b = $2"},
		{"postgres no placeholders", Postgres, "SELECT 1", "SELECT 1"},
		{"postgres many", Postgres, "?,?,?,?", "$1,$2,$3,$4"},
		{"sqlite unchanged", SQLite, "a = ? AND b = ?", "a = ? AND b = ?"},
		{"mysql unchanged", MySQL, "a = ? AND b = ?", "a = ? AND b = ?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLStore(nil, tt.dialect)
			if got := s.sqlstoreRebind(tt.query); got != tt.want {
				t.Fatalf("rebind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "10"},
		{123, "123"},
		{1000000, "1000000"},
	}
	for _, tt := range tests {
		if got := itoa(tt.in); got != tt.want {
			t.Fatalf("itoa(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSQLStoreStateArgs(t *testing.T) {
	tests := []struct {
		name      string
		states    []State
		wantMarks string
		wantArgs  []any
	}{
		{
			name:      "fetchable",
			states:    sqlstoreFetchableStates,
			wantMarks: "?, ?, ?",
			wantArgs:  []any{"available", "scheduled", "retryable"},
		},
		{
			name:      "unfinished",
			states:    sqlstoreUnfinishedStates,
			wantMarks: "?, ?, ?, ?",
			wantArgs:  []any{"available", "scheduled", "executing", "retryable"},
		},
		{
			name:      "finished",
			states:    sqlstoreFinishedStates,
			wantMarks: "?, ?, ?",
			wantArgs:  []any{"completed", "discarded", "cancelled"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marks, args := sqlstoreStateArgs(tt.states)
			if marks != tt.wantMarks {
				t.Fatalf("marks = %q, want %q", marks, tt.wantMarks)
			}
			if !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tt.wantArgs)
			}
		})
	}
}

func TestSQLStoreForUpdateAndLockRow(t *testing.T) {
	tests := []struct {
		name        string
		dialect     SQLDialect
		wantForUpd  string
		wantLockRow string
	}{
		{"postgres", Postgres, " FOR UPDATE SKIP LOCKED", " FOR UPDATE"},
		{"mysql", MySQL, " FOR UPDATE SKIP LOCKED", " FOR UPDATE"},
		{"sqlite", SQLite, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLStore(nil, tt.dialect)
			if got := s.sqlstoreForUpdate(); got != tt.wantForUpd {
				t.Fatalf("forUpdate = %q, want %q", got, tt.wantForUpd)
			}
			if got := s.sqlstoreLockRow(); got != tt.wantLockRow {
				t.Fatalf("lockRow = %q, want %q", got, tt.wantLockRow)
			}
		})
	}
}

func TestSQLStoreIndexName(t *testing.T) {
	tests := []struct {
		table  string
		suffix string
		want   string
	}{
		{"oban_jobs", "fetch", "idx_oban_jobs_fetch"},
		{"public.jobs", "unique", "idx_public_jobs_unique"},
		{"my-table", "fetch", "idx_my_table_fetch"},
	}
	for _, tt := range tests {
		t.Run(tt.table+"_"+tt.suffix, func(t *testing.T) {
			s := NewSQLStore(nil, Postgres, WithTableName(tt.table))
			if got := s.sqlstoreIndexName(tt.suffix); got != tt.want {
				t.Fatalf("indexName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLStoreDDL(t *testing.T) {
	wantColumns := []string{
		"id", "queue", "worker", "args", "max_attempts", "attempt", "priority",
		"state", "scheduled_at", "inserted_at", "attempted_at", "completed_at",
		"discarded_at", "unique_key", "unique_period_ns", "last_error", "errors",
		"tags", "meta",
	}

	tests := []struct {
		name       string
		dialect    SQLDialect
		wantIDType string
		wantINE    bool // CREATE INDEX IF NOT EXISTS expected
	}{
		{"postgres", Postgres, "BIGSERIAL PRIMARY KEY", true},
		{"sqlite", SQLite, "INTEGER PRIMARY KEY AUTOINCREMENT", true},
		{"mysql", MySQL, "BIGINT AUTO_INCREMENT PRIMARY KEY", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLStore(nil, tt.dialect)
			stmts := s.sqlstoreDDL()
			if len(stmts) != 3 {
				t.Fatalf("got %d statements, want 3", len(stmts))
			}
			create, fetchIdx, uniqueIdx := stmts[0], stmts[1], stmts[2]

			if !strings.Contains(create, "CREATE TABLE IF NOT EXISTS "+DefaultSQLTable) {
				t.Fatalf("create table missing header: %s", create)
			}
			if !strings.Contains(create, tt.wantIDType) {
				t.Fatalf("create table missing id type %q: %s", tt.wantIDType, create)
			}
			for _, col := range wantColumns {
				if !strings.Contains(create, col) {
					t.Fatalf("create table missing column %q", col)
				}
			}

			if !strings.Contains(fetchIdx, "(queue, state, priority, scheduled_at, id)") {
				t.Fatalf("fetch index has wrong columns: %s", fetchIdx)
			}
			if !strings.Contains(uniqueIdx, "(state, worker, unique_key)") {
				t.Fatalf("unique index has wrong columns: %s", uniqueIdx)
			}

			hasINE := strings.Contains(fetchIdx, "IF NOT EXISTS")
			if hasINE != tt.wantINE {
				t.Fatalf("fetch index IF NOT EXISTS = %v, want %v: %s", hasINE, tt.wantINE, fetchIdx)
			}
		})
	}
}

func TestSQLStoreMarshalErrors(t *testing.T) {
	at := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		errs []AttemptError
		want string
	}{
		{"nil", nil, "[]"},
		{"empty", []AttemptError{}, "[]"},
		{
			name: "one",
			errs: []AttemptError{{Attempt: 2, At: at, Message: "boom"}},
			want: `[{"attempt":2,"at":"2026-07-17T10:00:00Z","message":"boom"}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sqlstoreMarshalErrors(tt.errs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSQLStoreUnmarshalErrors(t *testing.T) {
	at := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		in      string
		want    []AttemptError
		wantErr bool
	}{
		{"empty bytes", "", nil, false},
		{"null", "null", nil, false},
		{"empty array", "[]", nil, false},
		{
			name: "one",
			in:   `[{"attempt":2,"at":"2026-07-17T10:00:00Z","message":"boom"}]`,
			want: []AttemptError{{Attempt: 2, At: at, Message: "boom"}},
		},
		{"invalid", "{not json", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sqlstoreUnmarshalErrors([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSQLStoreErrorsRoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 17, 12, 30, 0, 0, time.UTC)
	in := []AttemptError{
		{Attempt: 1, At: at, Message: "first"},
		{Attempt: 2, At: at.Add(time.Minute), Message: "second"},
	}
	b, err := sqlstoreMarshalErrors(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := sqlstoreUnmarshalErrors(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in = %v\nout = %v", in, out)
	}
}

func TestSQLStoreNullTime(t *testing.T) {
	ts := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		in        time.Time
		wantValid bool
	}{
		{"zero is null", time.Time{}, false},
		{"set is valid", ts, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nt := sqlstoreNullTime(tt.in)
			if nt.Valid != tt.wantValid {
				t.Fatalf("Valid = %v, want %v", nt.Valid, tt.wantValid)
			}
			back := sqlstoreTime(nt)
			if !back.Equal(tt.in) {
				t.Fatalf("round trip = %v, want %v", back, tt.in)
			}
		})
	}
}

func TestSQLStoreNullStr(t *testing.T) {
	tests := []struct {
		in        string
		wantValid bool
	}{
		{"", false},
		{"key", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			ns := sqlstoreNullStr(tt.in)
			if ns.Valid != tt.wantValid {
				t.Fatalf("Valid = %v, want %v", ns.Valid, tt.wantValid)
			}
			if ns.String != tt.in {
				t.Fatalf("String = %q, want %q", ns.String, tt.in)
			}
		})
	}
}
