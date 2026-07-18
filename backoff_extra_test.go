package oban

import (
	"testing"
	"time"
)

func TestConstantBackoff(t *testing.T) {
	b := NewConstantBackoff(2 * time.Second)
	for _, attempt := range []int{1, 2, 5, 100} {
		if got := b.Next(attempt); got != 2*time.Second {
			t.Errorf("attempt %d: got %v, want 2s", attempt, got)
		}
	}
	// Zero value defaults to one second.
	var zero ConstantBackoff
	if got := zero.Next(3); got != time.Second {
		t.Errorf("zero value: got %v, want 1s", got)
	}
}

func TestLinearBackoff(t *testing.T) {
	b := NewLinearBackoff(time.Second, 2*time.Second, time.Minute)
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second}, // base
		{2, 3 * time.Second}, // base + step
		{3, 5 * time.Second}, // base + 2*step
		{4, 7 * time.Second}, // base + 3*step
		{100, time.Minute},   // capped at max
		{0, 1 * time.Second}, // attempt<1 treated as 1
	}
	for _, tc := range tests {
		if got := b.Next(tc.attempt); got != tc.want {
			t.Errorf("attempt %d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestFibonacciBackoff(t *testing.T) {
	b := NewFibonacciBackoff(time.Second, time.Hour)
	// fib: 1,1,2,3,5,8,13,21...
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 1 * time.Second},
		{3, 2 * time.Second},
		{4, 3 * time.Second},
		{5, 5 * time.Second},
		{6, 8 * time.Second},
		{7, 13 * time.Second},
		{8, 21 * time.Second},
	}
	for _, tc := range tests {
		if got := b.Next(tc.attempt); got != tc.want {
			t.Errorf("attempt %d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
	// Large attempt is capped at max, not overflowed.
	if got := b.Next(1000); got != time.Hour {
		t.Errorf("attempt 1000: got %v, want 1h (capped)", got)
	}
}

func TestBackoffFunc(t *testing.T) {
	var b Backoff = BackoffFunc(func(attempt int) time.Duration {
		return time.Duration(attempt) * time.Second
	})
	if got := b.Next(4); got != 4*time.Second {
		t.Errorf("got %v, want 4s", got)
	}
}

func TestBackoffFib(t *testing.T) {
	want := []int64{1, 1, 1, 2, 3, 5, 8, 13, 21, 34, 55}
	for n := 1; n <= 10; n++ {
		if got := backoffFib(n); got != want[n] {
			t.Errorf("backoffFib(%d) = %d, want %d", n, got, want[n])
		}
	}
}

func BenchmarkFibonacciBackoff(b *testing.B) {
	bk := NewFibonacciBackoff(time.Second, time.Hour)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = bk.Next(i%40 + 1)
	}
}
