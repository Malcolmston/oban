package oban

import (
	"container/heap"
	"testing"
	"time"
)

func TestParseCronErrors(t *testing.T) {
	bad := []string{
		"",                // no fields
		"* * * *",         // 4 fields
		"* * * * * *",     // 6 fields
		"60 * * * *",      // minute out of range
		"* 24 * * *",      // hour out of range
		"* * 0 * *",       // dom below range
		"* * * 13 *",      // month out of range
		"* * * * 8x",      // bad value
		"5-1 * * * *",     // inverted range
		"*/0 * * * *",     // zero step
		"* * * bad *",     // bad month name
		"a * * * *",       // non-numeric
		"1,2,99 * * * *",  // list member out of range
		"* * * * mon-xyz", // bad dow name
	}
	for _, expr := range bad {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) expected error, got nil", expr)
		}
	}
}

func TestScheduleNext(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name string
		expr string
		from time.Time
		want time.Time
	}{
		{
			name: "every minute",
			expr: "* * * * *",
			from: time.Date(2026, 7, 17, 12, 30, 30, 0, loc),
			want: time.Date(2026, 7, 17, 12, 31, 0, 0, loc),
		},
		{
			name: "every 15 minutes",
			expr: "*/15 * * * *",
			from: time.Date(2026, 7, 17, 12, 31, 0, 0, loc),
			want: time.Date(2026, 7, 17, 12, 45, 0, 0, loc),
		},
		{
			name: "top of next hour",
			expr: "0 * * * *",
			from: time.Date(2026, 7, 17, 12, 30, 0, 0, loc),
			want: time.Date(2026, 7, 17, 13, 0, 0, 0, loc),
		},
		{
			name: "daily midnight",
			expr: "0 0 * * *",
			from: time.Date(2026, 7, 17, 12, 0, 0, 0, loc),
			want: time.Date(2026, 7, 18, 0, 0, 0, 0, loc),
		},
		{
			name: "weekdays at 9am, from friday",
			expr: "0 9 * * mon-fri",
			from: time.Date(2026, 7, 17, 10, 0, 0, 0, loc), // Fri
			want: time.Date(2026, 7, 20, 9, 0, 0, 0, loc),  // Mon
		},
		{
			name: "first of month",
			expr: "0 0 1 * *",
			from: time.Date(2026, 7, 17, 0, 0, 0, 0, loc),
			want: time.Date(2026, 8, 1, 0, 0, 0, 0, loc),
		},
		{
			name: "named month",
			expr: "0 0 1 jan *",
			from: time.Date(2026, 7, 17, 0, 0, 0, 0, loc),
			want: time.Date(2027, 1, 1, 0, 0, 0, 0, loc),
		},
		{
			name: "dom and dow when both set",
			expr: "0 0 13 * fri",                           // Oban AND rule: the 13th AND a Friday
			from: time.Date(2026, 7, 17, 0, 1, 0, 0, loc),  // Fri Jul 17
			want: time.Date(2026, 11, 13, 0, 0, 0, 0, loc), // next Friday the 13th
		},
		{
			name: "sunday as 0 and 7",
			expr: "0 12 * * 7",
			from: time.Date(2026, 7, 17, 0, 0, 0, 0, loc),  // Fri
			want: time.Date(2026, 7, 19, 12, 0, 0, 0, loc), // Sun
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q): %v", tt.expr, err)
			}
			got := s.Next(tt.from)
			if !got.Equal(tt.want) {
				t.Errorf("Next(%v) = %v, want %v", tt.from, got, tt.want)
			}
		})
	}
}

func TestScheduleNextStrictlyAfter(t *testing.T) {
	s := MustParseCron("30 12 * * *")
	at := time.Date(2026, 7, 17, 12, 30, 0, 0, time.UTC)
	got := s.Next(at)
	want := at.AddDate(0, 0, 1)
	if !got.Equal(want) {
		t.Errorf("Next at exact match = %v, want next day %v", got, want)
	}
}

func TestScheduleNextImpossible(t *testing.T) {
	// February 31st never occurs, so like Elixir Oban the expression is
	// rejected at parse time rather than parsing into a schedule that never
	// fires.
	if _, err := ParseCron("0 0 31 2 *"); err == nil {
		t.Fatal("ParseCron(\"0 0 31 2 *\") = nil error, want rejection of impossible day/month pairing")
	}
}

func TestCronHeapOrdering(t *testing.T) {
	h := &cronHeap{}
	heap.Init(h)
	now := baseTime
	entries := []*cronEntry{
		{next: now.Add(3 * time.Minute)},
		{next: now.Add(1 * time.Minute)},
		{next: now.Add(2 * time.Minute)},
		{next: now.Add(30 * time.Second)},
	}
	for _, e := range entries {
		heap.Push(h, e)
	}
	var got []time.Time
	for h.Len() > 0 {
		got = append(got, heap.Pop(h).(*cronEntry).next)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Before(got[i-1]) {
			t.Fatalf("heap popped out of order at %d: %v before %v", i, got[i], got[i-1])
		}
	}
}
