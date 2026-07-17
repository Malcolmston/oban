package oban

import "time"

// Clock abstracts the current time so that scheduling, backoff and unique
// windows can be tested deterministically. Production code uses [SystemClock];
// tests inject a controllable clock.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// SystemClock is a [Clock] backed by the wall clock.
type SystemClock struct{}

// Now returns time.Now.
func (SystemClock) Now() time.Time { return time.Now() }
