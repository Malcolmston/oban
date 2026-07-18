package oban

import "time"

// BackoffFunc adapts a plain function to the [Backoff] interface, mirroring the
// way [WorkerFunc] adapts a function to [Worker]. It lets a caller supply an
// ad-hoc retry policy without declaring a new type.
type BackoffFunc func(attempt int) time.Duration

// Next implements [Backoff] by calling f(attempt).
func (f BackoffFunc) Next(attempt int) time.Duration { return f(attempt) }

// ConstantBackoff retries with the same fixed delay after every failed attempt,
// regardless of the attempt number. It is the simplest policy and is useful when
// a downstream dependency recovers on a fixed cadence.
//
// A zero ConstantBackoff behaves as Delay=1s.
type ConstantBackoff struct {
	// Delay is the wait before every retry. Defaults to one second when
	// non-positive.
	Delay time.Duration
}

// NewConstantBackoff returns a [ConstantBackoff] with the given delay.
func NewConstantBackoff(delay time.Duration) *ConstantBackoff {
	return &ConstantBackoff{Delay: delay}
}

// Next implements [Backoff]. It returns the fixed delay for every attempt.
func (b *ConstantBackoff) Next(attempt int) time.Duration {
	d := b.Delay
	if d <= 0 {
		d = time.Second
	}
	return d
}

// LinearBackoff retries with a delay that grows linearly with the attempt
// number: the wait before retrying failed attempt n is
//
//	Base + Step*(n-1)
//
// capped at Max. Unlike [ExponentialBackoff] the growth is additive, which
// spreads retries out more gently for jobs expected to recover soon.
//
// A zero LinearBackoff behaves as Base=1s, Step=1s, Max=1h.
type LinearBackoff struct {
	// Base is the delay for the first retry. Defaults to one second when
	// non-positive.
	Base time.Duration
	// Step is added to the delay for each subsequent attempt. Defaults to one
	// second when non-positive.
	Step time.Duration
	// Max caps the computed delay. Defaults to one hour when non-positive.
	Max time.Duration
}

// NewLinearBackoff returns a [LinearBackoff] with the given parameters.
func NewLinearBackoff(base, step, max time.Duration) *LinearBackoff {
	return &LinearBackoff{Base: base, Step: step, Max: max}
}

// Next implements [Backoff]. Attempts below 1 are treated as 1.
func (b *LinearBackoff) Next(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := b.Base
	if base <= 0 {
		base = time.Second
	}
	step := b.Step
	if step <= 0 {
		step = time.Second
	}
	max := b.Max
	if max <= 0 {
		max = time.Hour
	}
	d := base + step*time.Duration(attempt-1)
	if d > max || d < 0 { // d < 0 guards against overflow.
		d = max
	}
	return d
}

// FibonacciBackoff retries with a delay that follows the Fibonacci sequence
// scaled by Unit: the wait before retrying failed attempt n is
//
//	Unit * fib(n)
//
// where fib(1)=fib(2)=1, fib(3)=2, fib(4)=3, fib(5)=5, and so on, capped at Max.
// Fibonacci growth sits between linear and exponential and is a common retry
// schedule for transient failures.
//
// A zero FibonacciBackoff behaves as Unit=1s, Max=1h.
type FibonacciBackoff struct {
	// Unit scales each Fibonacci number into a duration. Defaults to one second
	// when non-positive.
	Unit time.Duration
	// Max caps the computed delay. Defaults to one hour when non-positive.
	Max time.Duration
}

// NewFibonacciBackoff returns a [FibonacciBackoff] with the given parameters.
func NewFibonacciBackoff(unit, max time.Duration) *FibonacciBackoff {
	return &FibonacciBackoff{Unit: unit, Max: max}
}

// Next implements [Backoff]. Attempts below 1 are treated as 1.
func (b *FibonacciBackoff) Next(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	unit := b.Unit
	if unit <= 0 {
		unit = time.Second
	}
	max := b.Max
	if max <= 0 {
		max = time.Hour
	}
	fib := backoffFib(attempt)
	// Guard the multiplication against overflow before scaling.
	if fib > int64(max/unit) {
		return max
	}
	d := unit * time.Duration(fib)
	if d > max || d < 0 {
		d = max
	}
	return d
}

// backoffFib returns the n-th Fibonacci number with fib(1)=fib(2)=1. It caps
// growth once the value exceeds a large bound so that an out-of-range attempt
// cannot overflow; the caller clamps the resulting delay to Max regardless.
func backoffFib(n int) int64 {
	if n <= 2 {
		return 1
	}
	const cap = int64(1) << 62
	var a, b int64 = 1, 1
	for i := 3; i <= n; i++ {
		a, b = b, a+b
		if b >= cap {
			return cap
		}
	}
	return b
}
