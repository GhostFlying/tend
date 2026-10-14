// Package testkit provides deterministic stand-ins for tests: a controllable
// clock, reproducible identifier generation and, later, fake harnesses and
// runners.
package testkit

import (
	"sync"
	"time"
)

// Clock is a manually advanced clock that is safe for concurrent use.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Epoch is the default start time of a Clock: 2026-01-01T00:00:00Z.
var Epoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// NewClock returns a clock that starts at Epoch.
func NewClock() *Clock { return &Clock{now: Epoch} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
