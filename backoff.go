package oban

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Backoff computes how long to wait before the next attempt of a failed job.
type Backoff interface {
	// Next returns the delay before the given 1-based attempt number is retried.
	// It is called with the attempt that just failed.
	Next(attempt int) time.Duration
}

// randFloat abstracts a source of random numbers in [0, 1) so that jitter can be
// made deterministic in tests.
type randFloat interface {
	Float64() float64
}

// ExponentialBackoff retries with an exponentially growing delay and optional
// jitter. The base delay for a failed attempt n is:
//
//	Base * 2^(n-1)
//
// capped at Max. Jitter (a fraction in [0, 1]) then reduces the delay by a
// random amount so that many jobs failing together do not retry in lockstep:
// the returned delay is uniformly distributed in
//
//	[ d * (1 - Jitter), d ]
//
// A zero ExponentialBackoff is usable and behaves as Base=1s, Max=5m, no jitter.
type ExponentialBackoff struct {
	// Base is the delay for the first retry. Defaults to one second.
	Base time.Duration
	// Max caps the computed delay. Defaults to five minutes.
	Max time.Duration
	// Jitter is the fraction of the delay that may be randomly subtracted. It
	// is clamped to [0, 1]. Defaults to zero (no jitter).
	Jitter float64

	mu   sync.Mutex
	rand randFloat
}

// NewExponentialBackoff returns an [ExponentialBackoff] with the given
// parameters and a jitter source seeded from seed for reproducibility.
func NewExponentialBackoff(base, max time.Duration, jitter float64, seed int64) *ExponentialBackoff {
	return &ExponentialBackoff{
		Base:   base,
		Max:    max,
		Jitter: jitter,
		rand:   rand.New(rand.NewSource(seed)), //nolint:gosec // jitter, not security-sensitive
	}
}

// Next implements [Backoff].
func (b *ExponentialBackoff) Next(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := b.Base
	if base <= 0 {
		base = time.Second
	}
	max := b.Max
	if max <= 0 {
		max = 5 * time.Minute
	}

	// Compute base * 2^(attempt-1) guarding against overflow.
	d := float64(base) * math.Pow(2, float64(attempt-1))
	if d > float64(max) || math.IsInf(d, 1) {
		d = float64(max)
	}

	jitter := b.Jitter
	if jitter > 0 {
		if jitter > 1 {
			jitter = 1
		}
		d -= d * jitter * b.next()
	}
	return time.Duration(d)
}

// next returns a random float in [0, 1) from the configured source, defaulting
// to the shared global source when none was injected.
func (b *ExponentialBackoff) next() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rand == nil {
		b.rand = rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // jitter, not security-sensitive
	}
	return b.rand.Float64()
}
