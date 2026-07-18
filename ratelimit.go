package oban

import (
	"sync"
	"time"
)

// RateLimiter is a deterministic token-bucket rate limiter suitable for
// throttling job execution against a downstream dependency's capacity. It
// refills Rate tokens over each Interval and admits work only while tokens
// remain, mirroring the rate-limiting Elixir Oban Pro applies to queues.
//
// The limiter draws the current time from an injected [Clock], so tests can
// advance time explicitly and observe exact refill behavior. It is safe for
// concurrent use.
//
// A zero RateLimiter is not usable; construct one with [NewRateLimiter].
type RateLimiter struct {
	rate     float64
	interval time.Duration
	clock    Clock

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a token-bucket limiter that admits up to rate units of
// work per interval. The bucket starts full (rate tokens). clock supplies the
// current time; a nil clock defaults to [SystemClock]. It panics if rate is not
// positive or interval is not positive, since those are programmer errors.
func NewRateLimiter(rate int, interval time.Duration, clock Clock) *RateLimiter {
	if rate <= 0 {
		panic("oban: rate limiter rate must be positive")
	}
	if interval <= 0 {
		panic("oban: rate limiter interval must be positive")
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &RateLimiter{
		rate:     float64(rate),
		interval: interval,
		clock:    clock,
		tokens:   float64(rate),
		last:     clock.Now(),
	}
}

// refill adds the tokens accrued since the last observation, capping the bucket
// at its burst capacity (rate). The caller must hold the mutex.
func (r *RateLimiter) refill(now time.Time) {
	elapsed := now.Sub(r.last)
	if elapsed <= 0 {
		return
	}
	r.last = now
	r.tokens += r.rate * (float64(elapsed) / float64(r.interval))
	if r.tokens > r.rate {
		r.tokens = r.rate
	}
}

// Allow reports whether one unit of work may proceed now, consuming a token when
// it can. It is shorthand for AllowN(1).
func (r *RateLimiter) Allow() bool {
	return r.AllowN(1)
}

// AllowN reports whether n units of work may proceed now, consuming n tokens
// when they can. It first credits the bucket for elapsed time, then consumes n
// tokens only if at least n are available; otherwise it consumes nothing and
// returns false. A non-positive n always returns true and consumes nothing.
func (r *RateLimiter) AllowN(n int) bool {
	if n <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill(r.clock.Now())
	if r.tokens >= float64(n) {
		r.tokens -= float64(n)
		return true
	}
	return false
}

// Tokens returns the number of whole tokens currently available, after crediting
// the bucket for time elapsed since the last call. It is primarily useful for
// introspection and tests.
func (r *RateLimiter) Tokens() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill(r.clock.Now())
	return int(r.tokens)
}
