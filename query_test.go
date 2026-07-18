package oban

import (
	"context"
	"testing"
	"time"
)

func intptr(n int) *int { return &n }

func seedStore(t *testing.T) *InMemoryStore {
	t.Helper()
	s := NewInMemoryStore()
	specs := []struct {
		worker   string
		queue    string
		priority int
	}{
		{"email", "mailers", 0},
		{"email", "mailers", 5},
		{"report", "default", 1},
		{"report", "default", 9},
	}
	for _, sp := range specs {
		j, _ := NewJob(sp.worker, map[string]int{"n": sp.priority},
			WithQueue(sp.queue), WithPriority(sp.priority))
		j.InsertedAt = baseTime
		if _, _, err := s.Enqueue(context.Background(), j); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	return s
}

func TestJobFilterMatches(t *testing.T) {
	j := &Job{Queue: "mailers", Worker: "email", Priority: 3, State: StateAvailable, InsertedAt: baseTime}
	tests := []struct {
		name   string
		filter JobFilter
		want   bool
	}{
		{"empty matches", JobFilter{}, true},
		{"queue match", JobFilter{Queue: "mailers"}, true},
		{"queue miss", JobFilter{Queue: "other"}, false},
		{"worker match", JobFilter{Worker: "email"}, true},
		{"worker miss", JobFilter{Worker: "sms"}, false},
		{"state match", JobFilter{States: []State{StateAvailable, StateScheduled}}, true},
		{"state miss", JobFilter{States: []State{StateCompleted}}, false},
		{"min priority ok", JobFilter{MinPriority: intptr(3)}, true},
		{"min priority miss", JobFilter{MinPriority: intptr(4)}, false},
		{"max priority ok", JobFilter{MaxPriority: intptr(3)}, true},
		{"max priority miss", JobFilter{MaxPriority: intptr(2)}, false},
		{"inserted after miss", JobFilter{InsertedAfter: baseTime}, false},
		{"inserted after ok", JobFilter{InsertedAfter: baseTime.Add(-time.Hour)}, true},
		{"combined miss", JobFilter{Queue: "mailers", Worker: "sms"}, false},
	}
	for _, tc := range tests {
		if got := tc.filter.Matches(j); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if (JobFilter{}).Matches(nil) {
		t.Error("nil job should not match")
	}
}

func TestListJobsAndCount(t *testing.T) {
	ctx := context.Background()
	s := seedStore(t)

	mail, err := ListJobs(ctx, s, JobFilter{Queue: "mailers"})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(mail) != 2 {
		t.Fatalf("mailers jobs = %d, want 2", len(mail))
	}
	// Ordered by ascending ID.
	if mail[0].ID > mail[1].ID {
		t.Error("jobs not ordered by ID")
	}

	n, err := CountJobs(ctx, s, JobFilter{Worker: "report", MinPriority: intptr(5)})
	if err != nil {
		t.Fatalf("CountJobs: %v", err)
	}
	if n != 1 {
		t.Errorf("high-priority reports = %d, want 1", n)
	}

	all, _ := CountJobs(ctx, s, JobFilter{})
	if all != 4 {
		t.Errorf("all jobs = %d, want 4", all)
	}
}

func TestInMemoryStoreIntrospection(t *testing.T) {
	s := seedStore(t)
	if s.Len() != 4 {
		t.Errorf("Len = %d, want 4", s.Len())
	}
	if s.CountByQueue("default") != 2 {
		t.Errorf("CountByQueue(default) = %d, want 2", s.CountByQueue("default"))
	}
	states := s.States()
	if states[StateAvailable] != 4 {
		t.Errorf("available = %d, want 4", states[StateAvailable])
	}
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", s.Len())
	}
	// IDs keep advancing after Clear.
	j, _ := NewJob("w", nil)
	j.InsertedAt = baseTime
	stored, _, _ := s.Enqueue(context.Background(), j)
	if stored.ID != 5 {
		t.Errorf("id after clear = %d, want 5 (counter not reset)", stored.ID)
	}
}

func BenchmarkInMemoryList(b *testing.B) {
	ctx := context.Background()
	s := NewInMemoryStore()
	for i := 0; i < 1000; i++ {
		j, _ := NewJob("w", nil, WithQueue("q"))
		j.InsertedAt = baseTime
		_, _, _ = s.Enqueue(ctx, j)
	}
	filter := JobFilter{Queue: "q", States: []State{StateAvailable}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = s.List(ctx, filter)
	}
}
