package nativeusage

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Wait blocks indefinitely when there is no active work. A lifecycle notification
// must be buffered (or captured before reading work) to avoid lost wakeups.
func Wait(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
	var tick <-chan time.Time
	var timer *time.Timer
	if delay > 0 {
		timer = time.NewTimer(delay)
		tick = timer.C
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-tick:
		return true
	}
}

type retryEntry struct {
	at    int64
	delay time.Duration
}

// Retries bounds failures per source/request without delaying healthy sources.
// It is owned by one collector goroutine; clocks are supplied by its caller.
type Retries map[string]retryEntry

func (r Retries) Due(key string, now int64) bool { return r[key].at <= now }
func (r Retries) Failed(key string, now int64) {
	d := r[key].delay * 2
	if d < 2*time.Second {
		d = 2 * time.Second
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	r[key] = retryEntry{at: now + d.Milliseconds(), delay: d}
}
func (r Retries) Succeeded(key string) { delete(r, key) }

// Collection restores the durable reading deadline after restart. Terminal
// sources cannot leave an expired in-memory retry keeping a collector awake.
func (r Retries) Collection(key string, c protocol.UsageCollection) {
	if !c.Reading() || c.NextRetryAt == 0 {
		delete(r, key)
		return
	}
	r[key] = retryEntry{at: c.NextRetryAt, delay: time.Duration(c.RetryDelayMillis) * time.Millisecond}
}
func (r Retries) Delay(now int64, active bool) time.Duration {
	var delay time.Duration
	if active {
		delay = 2 * time.Second
	}
	for _, e := range r {
		d := time.Duration(max(1, e.at-now)) * time.Millisecond
		if delay == 0 || d < delay {
			delay = d
		}
	}
	return delay
}

func (r Retries) Retain(keys map[string]bool) {
	for key := range r {
		if !keys[key] {
			delete(r, key)
		}
	}
}
