package oban

import (
	"io"
	"log"
	"sync"
	"time"
)

// fakeClock is a manually-advanced [Clock] for deterministic tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// discardLogger is a logger that swallows output, keeping test logs quiet.
func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// baseTime is a fixed reference instant used across time-sensitive tests.
var baseTime = time.Date(2026, time.July, 17, 12, 30, 0, 0, time.UTC)
