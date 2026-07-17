package oban

import (
	"testing"
	"time"
)

func TestExponentialBackoffSchedule(t *testing.T) {
	b := &ExponentialBackoff{Base: time.Second, Max: time.Minute, Jitter: 0}
	want := []time.Duration{
		time.Second,      // attempt 1: base * 2^0
		2 * time.Second,  // attempt 2: base * 2^1
		4 * time.Second,  // attempt 3
		8 * time.Second,  // attempt 4
		16 * time.Second, // attempt 5
		32 * time.Second, // attempt 6
		time.Minute,      // attempt 7: 64s capped to Max
		time.Minute,      // attempt 8: capped
	}
	for i, w := range want {
		attempt := i + 1
		if got := b.Next(attempt); got != w {
			t.Errorf("Next(%d) = %v, want %v", attempt, got, w)
		}
	}
}

func TestExponentialBackoffDefaults(t *testing.T) {
	var b ExponentialBackoff // zero value
	if got := b.Next(1); got != time.Second {
		t.Errorf("zero-value Next(1) = %v, want 1s", got)
	}
	if got := b.Next(0); got != time.Second {
		t.Errorf("Next(0) should clamp to attempt 1, got %v", got)
	}
	if got := b.Next(100); got != 5*time.Minute {
		t.Errorf("Next(100) = %v, want default Max 5m", got)
	}
}

func TestExponentialBackoffJitterBounds(t *testing.T) {
	b := NewExponentialBackoff(time.Second, time.Minute, 0.5, 42)
	for attempt := 1; attempt <= 6; attempt++ {
		base := &ExponentialBackoff{Base: time.Second, Max: time.Minute}
		full := base.Next(attempt)
		lo := time.Duration(float64(full) * 0.5)
		for i := 0; i < 100; i++ {
			got := b.Next(attempt)
			if got < lo || got > full {
				t.Fatalf("attempt %d: jittered %v out of [%v, %v]", attempt, got, lo, full)
			}
		}
	}
}

func TestExponentialBackoffJitterDeterministic(t *testing.T) {
	b1 := NewExponentialBackoff(time.Second, time.Minute, 0.3, 7)
	b2 := NewExponentialBackoff(time.Second, time.Minute, 0.3, 7)
	for attempt := 1; attempt <= 10; attempt++ {
		if b1.Next(attempt) != b2.Next(attempt) {
			t.Fatalf("same seed produced different delays at attempt %d", attempt)
		}
	}
}
