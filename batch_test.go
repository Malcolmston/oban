package oban

import (
	"context"
	"testing"
	"time"
)

func TestInsertMany(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	jobs := make([]*Job, 3)
	for i := range jobs {
		j, _ := NewJob("w", map[string]int{"i": i})
		j.InsertedAt = baseTime
		jobs[i] = j
	}
	results, err := InsertMany(ctx, s, jobs)
	if err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	for i, r := range results {
		if !r.Inserted {
			t.Errorf("result %d not inserted", i)
		}
		if r.Job.ID == 0 {
			t.Errorf("result %d has no ID", i)
		}
	}
	if s.Len() != 3 {
		t.Errorf("store has %d, want 3", s.Len())
	}
}

func TestInsertManyDeduplicates(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	mk := func() *Job {
		j, _ := NewJob("w", map[string]int{"x": 1}, WithUnique("k", time.Hour))
		j.InsertedAt = baseTime
		return j
	}
	results, err := InsertMany(ctx, s, []*Job{mk(), mk()})
	if err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	if !results[0].Inserted {
		t.Error("first should be inserted")
	}
	if results[1].Inserted {
		t.Error("second should be de-duplicated")
	}
	if s.Len() != 1 {
		t.Errorf("store has %d, want 1", s.Len())
	}
}

func TestInsertManyNilError(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	j, _ := NewJob("w", nil)
	j.InsertedAt = baseTime
	results, err := InsertMany(ctx, s, []*Job{j, nil})
	if err == nil {
		t.Fatal("expected error for nil job")
	}
	// The first job was still inserted before the failure.
	if len(results) != 1 {
		t.Errorf("results = %d, want 1", len(results))
	}
}

func TestEnqueueMany(t *testing.T) {
	ctx := context.Background()
	eng, err := New(Config{Store: NewInMemoryStore(), Clock: newFakeClock(baseTime)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	jobs := []*Job{}
	for i := 0; i < 2; i++ {
		j, _ := NewJob("w", map[string]int{"i": i})
		jobs = append(jobs, j)
	}
	results, err := eng.EnqueueMany(ctx, jobs)
	if err != nil {
		t.Fatalf("EnqueueMany: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	// The engine stamped InsertedAt from its clock.
	for i, r := range results {
		if !r.Job.InsertedAt.Equal(baseTime) {
			t.Errorf("result %d InsertedAt = %v, want %v", i, r.Job.InsertedAt, baseTime)
		}
	}
}
