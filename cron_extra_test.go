package oban

import (
	"testing"
	"time"
)

func TestParseCronSpecMacros(t *testing.T) {
	tests := []struct {
		spec string
		// A time known to match the resulting schedule.
		match time.Time
	}{
		{"@hourly", time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)},
		{"@daily", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)},
		{"@MIDNIGHT", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)},
		{"@weekly", time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)},  // 2026-03-08 is a Sunday
		{"@monthly", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}, // first of month
		{"@yearly", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"@annually", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		s, err := ParseCronSpec(tc.spec)
		if err != nil {
			t.Fatalf("ParseCronSpec(%q): %v", tc.spec, err)
		}
		if !s.Matches(tc.match) {
			t.Errorf("%q: expected %v to match", tc.spec, tc.match)
		}
	}
}

func TestParseCronSpecPlainAndError(t *testing.T) {
	if _, err := ParseCronSpec("*/15 * * * *"); err != nil {
		t.Fatalf("plain expression: %v", err)
	}
	if _, err := ParseCronSpec("@bogus"); err == nil {
		t.Fatal("expected error for unknown macro")
	}
	if _, err := ParseCronSpec("1 2 3"); err == nil {
		t.Fatal("expected error for malformed expression")
	}
}

func TestScheduleMatches(t *testing.T) {
	s := MustParseCron("30 9 * * mon-fri")
	// Monday 2026-03-02 at 09:30 matches.
	if !s.Matches(time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC)) {
		t.Error("weekday 09:30 should match")
	}
	// Seconds are ignored.
	if !s.Matches(time.Date(2026, 3, 2, 9, 30, 45, 0, time.UTC)) {
		t.Error("seconds should be ignored")
	}
	// Wrong minute.
	if s.Matches(time.Date(2026, 3, 2, 9, 31, 0, 0, time.UTC)) {
		t.Error("09:31 should not match")
	}
	// Saturday 2026-03-07 must not match.
	if s.Matches(time.Date(2026, 3, 7, 9, 30, 0, 0, time.UTC)) {
		t.Error("Saturday should not match")
	}
}

func TestScheduleUpcoming(t *testing.T) {
	s := MustParseCron("0 * * * *") // top of every hour
	start := time.Date(2026, 3, 2, 10, 15, 0, 0, time.UTC)
	got := s.Upcoming(start, 3)
	want := []time.Time{
		time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 2, 13, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d times, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("upcoming[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if s.Upcoming(start, 0) != nil {
		t.Error("Upcoming(_, 0) should be nil")
	}
}

func TestSchedulePrev(t *testing.T) {
	s := MustParseCron("0 9 * * *") // 09:00 daily
	from := time.Date(2026, 3, 2, 10, 15, 0, 0, time.UTC)
	prev := s.Prev(from)
	want := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	if !prev.Equal(want) {
		t.Errorf("Prev = %v, want %v", prev, want)
	}

	// From exactly 09:00 the previous match is the day before (strictly before).
	prev2 := s.Prev(want)
	want2 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	if !prev2.Equal(want2) {
		t.Errorf("Prev(09:00) = %v, want %v", prev2, want2)
	}
}

func TestPrevNextRoundTrip(t *testing.T) {
	s := MustParseCron("*/15 * * * *")
	base := time.Date(2026, 3, 2, 10, 7, 0, 0, time.UTC)
	next := s.Next(base)
	// Prev of a moment just after a fire time returns that same fire time.
	if got := s.Prev(next.Add(time.Minute)); !got.Equal(next) {
		t.Errorf("Prev(next+1m) = %v, want %v", got, next)
	}
}

func TestMustParseCronSpecPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	MustParseCronSpec("@nope")
}

func BenchmarkScheduleMatches(b *testing.B) {
	s := MustParseCron("30 9 * * mon-fri")
	t := time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Matches(t)
	}
}
