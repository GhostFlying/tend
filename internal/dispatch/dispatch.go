// Package dispatch delivers outbox messages after their transaction has
// committed.
//
// Messages of one destination are delivered one at a time in queue order
// (main-thread owner work precedes background events); destinations are
// independent of each other. Every message keeps the child
// request ID allocated when it was queued, so a receiver can deduplicate
// redeliveries. A message found in the middle of a delivery attempt after a
// restart is reconciled by its handler and never resent blindly: when the
// handler cannot tell whether it took effect, the message is marked unknown
// and left to the owner.
package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
)

// Handler delivers the messages of one destination.
type Handler interface {
	// Deliver sends the message. The receiver must use d.RequestID as its
	// idempotency key.
	Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult
	// Reconcile decides what happened to a message whose delivery attempt
	// was cut short by a restart: delivered, certainly not delivered
	// (Retry), failed, or unknown.
	Reconcile(ctx context.Context, d app.Delivery) app.DeliveryResult
}

// Config tunes the dispatcher. Zero values select the defaults.
type Config struct {
	// PollInterval bounds how long the dispatcher sleeps without a
	// notification. Default 30s.
	PollInterval time.Duration
	// BaseBackoff is the delay after the first failed attempt; it doubles
	// with every further attempt. Default 1s.
	BaseBackoff time.Duration
	// MaxBackoff caps the delay between attempts. Default 1m.
	MaxBackoff time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 30 * time.Second
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Minute
	}
	return c
}

// Backoff returns the delay before the attempt after the given one.
func (c Config) Backoff(attempt int64) time.Duration {
	c = c.withDefaults()
	d := c.BaseBackoff
	for i := int64(1); i < attempt && d < c.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, c.MaxBackoff)
}

// Signal wakes the dispatcher. It implements app.Notifier; notifications
// that arrive while one is pending are merged.
type Signal struct {
	ch chan struct{}
}

// NewSignal returns a Signal.
func NewSignal() *Signal { return &Signal{ch: make(chan struct{}, 1)} }

// Notify implements app.Notifier.
func (s *Signal) Notify() {
	select {
	case s.ch <- struct{}{}:
	default:
	}
}

// Dispatcher delivers outbox messages through destination handlers.
type Dispatcher struct {
	app      *app.App
	handlers map[string]Handler
	signal   *Signal
	now      func() time.Time
	cfg      Config
	log      *slog.Logger
}

// New returns a Dispatcher. Messages for destinations without a handler
// stay queued.
func New(a *app.App, handlers map[string]Handler, signal *Signal, now func() time.Time, cfg Config, log *slog.Logger) *Dispatcher {
	return &Dispatcher{app: a, handlers: handlers, signal: signal, now: now, cfg: cfg.withDefaults(), log: log}
}

// Recover reconciles, for every destination with a handler, the messages
// left in the middle of a delivery attempt, by a previous process or by an
// attempt whose result could not be recorded. It must not run concurrently
// with a drain of the same destination.
func (d *Dispatcher) Recover(ctx context.Context) error {
	var errs []error
	for dest, h := range d.handlers {
		errs = append(errs, d.recoverDestination(ctx, dest, h))
	}
	return errors.Join(errs...)
}

func (d *Dispatcher) recoverDestination(ctx context.Context, dest string, h Handler) error {
	inflight, err := d.app.InFlightDeliveries(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range inflight {
		if m.Destination != dest {
			continue
		}
		r := h.Reconcile(ctx, m)
		d.log.InfoContext(ctx, "reconciled outbox message", "destination", dest, "request_id", m.RequestID, "outcome", r.Outcome)
		if err := d.app.FinishDelivery(ctx, m, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Drain delivers every due message of every destination, one destination
// per goroutine, until each destination has nothing due. It returns the
// earliest time a queued message becomes due, or the zero time.
func (d *Dispatcher) Drain(ctx context.Context) (time.Time, error) {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wake time.Time
		errs []error
	)
	for dest, h := range d.handlers {
		wg.Go(func() {
			w, err := d.drainDestination(ctx, dest, h)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if !w.IsZero() && (wake.IsZero() || w.Before(wake)) {
				wake = w
			}
		})
	}
	wg.Wait()
	return wake, errors.Join(errs...)
}

func (d *Dispatcher) drainDestination(ctx context.Context, dest string, h Handler) (time.Time, error) {
	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		m, wake, ok, err := d.app.NextDelivery(ctx, dest)
		if err != nil || !ok {
			return wake, err
		}
		r := h.Deliver(ctx, m)
		if r.Outcome == app.Retry {
			r.RetryAfter = max(r.RetryAfter, d.cfg.Backoff(m.Attempt))
		}
		if r.Outcome != app.Delivered {
			d.log.WarnContext(ctx, "outbox delivery not completed", "destination", dest, "request_id", m.RequestID,
				"attempt", m.Attempt, "outcome", r.Outcome, "reason", r.Reason)
		}
		// When ctx is cancelled mid-delivery this fails and the message
		// stays in sending; the next Recover reconciles it.
		if err := d.app.FinishDelivery(ctx, m, r); err != nil {
			return time.Time{}, err
		}
	}
}

// Run delivers messages as they are queued until ctx is cancelled, and
// returns ctx's error. Each destination has its own worker, so a slow
// handler never delays another destination. Before every drain a worker
// reconciles its destination's messages left in the middle of a delivery
// attempt: drains of a destination are sequential, so any such message
// belongs to a previous process or to an attempt whose result could not be
// recorded.
func (d *Dispatcher) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wakes := make([]chan struct{}, 0, len(d.handlers))
	for dest, h := range d.handlers {
		wake := make(chan struct{}, 1)
		wakes = append(wakes, wake)
		wg.Go(func() { d.work(ctx, dest, h, wake) })
	}
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.signal.ch:
			for _, w := range wakes {
				select {
				case w <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (d *Dispatcher) work(ctx context.Context, dest string, h Handler, wake <-chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		if err := d.recoverDestination(ctx, dest, h); err != nil && ctx.Err() == nil {
			d.log.ErrorContext(ctx, "outbox recovery failed", "destination", dest, "err", err)
		}
		due, err := d.drainDestination(ctx, dest, h)
		if err != nil && ctx.Err() == nil {
			d.log.ErrorContext(ctx, "outbox drain failed", "destination", dest, "err", err)
		}
		wait := d.cfg.PollInterval
		if !due.IsZero() {
			wait = min(wait, max(due.Sub(d.now()), 0))
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
	}
}
