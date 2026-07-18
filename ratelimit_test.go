package oban

import (
	"testing"
	"time"
)

func TestRateLimiterBurst(t *testing.T) {
	clock := newFakeClock(baseTime)
	rl := NewRateLimiter(3, time.Second, clock)

	// Bucket starts full: three immediate allows, then denied.
	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("allow %d should succeed", i)
		}
	}
	if rl.Allow() {
		t.Fatal("fourth allow should be denied")
	}
}

func TestRateLimiterRefill(t *testing.T) {
	clock := newFakeClock(baseTime)
	rl := NewRateLimiter(2, time.Second, clock)

	// Drain the bucket.
	rl.Allow()
	rl.Allow()
	if rl.Allow() {
		t.Fatal("should be empty")
	}

	// After half the interval, one token (rate=2 per second => 1 per 500ms).
	clock.Advance(500 * time.Millisecond)
	if !rl.Allow() {
		t.Fatal("expected one token after 500ms")
	}
	if rl.Allow() {
		t.Fatal("should be empty again")
	}

	// After a full interval the bucket refills to its cap, not beyond.
	clock.Advance(10 * time.Second)
	if got := rl.Tokens(); got != 2 {
		t.Fatalf("tokens = %d, want 2 (capped)", got)
	}
}

func TestRateLimiterAllowN(t *testing.T) {
	clock := newFakeClock(baseTime)
	rl := NewRateLimiter(5, time.Second, clock)

	if !rl.AllowN(3) {
		t.Fatal("AllowN(3) should succeed with 5 tokens")
	}
	if rl.AllowN(3) {
		t.Fatal("AllowN(3) should fail with 2 tokens left")
	}
	if !rl.AllowN(2) {
		t.Fatal("AllowN(2) should succeed with 2 tokens left")
	}
	// Non-positive n is always allowed and consumes nothing.
	if !rl.AllowN(0) {
		t.Fatal("AllowN(0) should be allowed")
	}
}

func TestRateLimiterPanics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rate     int
		interval time.Duration
	}{
		{"zero rate", 0, time.Second},
		{"zero interval", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			NewRateLimiter(tc.rate, tc.interval, nil)
		})
	}
}

func BenchmarkRateLimiterAllow(b *testing.B) {
	rl := NewRateLimiter(1_000_000_000, time.Second, newFakeClock(baseTime))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = rl.Allow()
	}
}
