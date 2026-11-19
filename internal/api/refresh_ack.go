package api

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// refreshAcks coalesces witnessed cursors from all streams for this store.
// The worker owns writes independently of any single HTTP request.
type refreshAcks struct {
	ctx    context.Context
	cancel context.CancelFunc
	ack    func(context.Context, int64) error
	log    *slog.Logger
	wake   chan struct{}

	mu      sync.Mutex
	through int64
	done    chan struct{}
}

func newRefreshAcks(ack func(context.Context, int64) error, log *slog.Logger) *refreshAcks {
	ctx, cancel := context.WithCancel(context.Background())
	return &refreshAcks{ctx: ctx, cancel: cancel, ack: ack, log: log, wake: make(chan struct{}, 1)}
}

func (a *refreshAcks) offer(through int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if through <= a.through || a.ctx.Err() != nil {
		return
	}
	a.through = through
	if a.done == nil {
		a.done = make(chan struct{})
		go a.run(a.done)
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *refreshAcks) stop() {
	a.mu.Lock()
	a.cancel()
	done := a.done
	a.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (a *refreshAcks) run(done chan<- struct{}) {
	defer close(done)
	var confirmed int64
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.wake:
		}
		failed := false
		for a.ctx.Err() == nil {
			a.mu.Lock()
			through := a.through
			a.mu.Unlock()
			if through <= confirmed {
				break
			}
			if err := a.ack(a.ctx, through); err != nil {
				if !failed && a.ctx.Err() == nil {
					a.log.WarnContext(a.ctx, "event refresh acknowledgement failed", "through", through, "err", err)
				}
				failed = true
				select {
				case <-a.ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			confirmed = through
			failed = false
		}
	}
}
