package mainthread

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
)

// BackgroundPool bounds isolated harness processes; each logical session is serial.
type BackgroundPool struct {
	App    *app.App
	Front  *Manager
	Limit  int
	New    func(string) (*Manager, error)
	mu     sync.Mutex
	active map[string]*Manager
}

func (p *BackgroundPool) Run(ctx context.Context) {
	limit := p.Limit
	if limit <= 0 {
		limit = 2
	}
	p.mu.Lock()
	p.active = map[string]*Manager{}
	p.mu.Unlock()
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		ids, err := p.App.PendingAgentConversations(ctx)
		if err == nil {
			for _, id := range ids {
				p.mu.Lock()
				if len(p.active) >= limit || p.active[id] != nil {
					p.mu.Unlock()
					continue
				}
				paused, err := p.App.UpgradePausesMain(ctx)
				if err != nil || paused {
					p.mu.Unlock()
					break
				}
				m, err := p.New(id)
				if err != nil {
					p.mu.Unlock()
					continue
				}
				p.active[id] = m
				p.mu.Unlock()
				wg.Go(func() { defer func() { p.mu.Lock(); delete(p.active, id); p.mu.Unlock() }(); p.drain(ctx, id, m) })
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (p *BackgroundPool) drain(ctx context.Context, id string, m *Manager) {
	receiptRead, err := p.App.DeferAgentReceiptRead(ctx, id)
	if err != nil {
		return
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(run) }()
	defer func() { cancel(); <-done }()
	// Startup settles abandoned turns before reconciliation can acknowledge
	// their deliveries. Do not tear down an uninitialized recovery process.
	waitCtx := run
	stopWait := func() {}
	if receiptRead {
		waitCtx, stopWait = context.WithTimeout(run, 20*time.Second)
	}
	_, err = m.waitThread(waitCtx)
	stopWait()
	if err != nil {
		return
	}
	dest := "background_thread:" + id
	inflight, err := p.App.InFlightDeliveries(run)
	if err != nil {
		return
	}
	for _, d := range inflight {
		if d.Destination == dest {
			if err := p.App.FinishDelivery(run, d, m.Reconcile(run, d)); err != nil {
				return
			}
		}
	}
	for run.Err() == nil {
		if paused, err := p.App.UpgradePausesMain(run); err != nil || paused {
			return
		}
		d, _, ok, err := p.App.NextDelivery(run, dest)
		if err != nil || !ok {
			return
		}
		result := m.Deliver(run, d)
		if d.Operation == app.OpRecoverAgent {
			pending, err := p.App.NativeReceiptPending(run, d.RequestID)
			if err != nil {
				return
			}
			if pending {
				// Let the replacement session read the known native execution
				// before releasing this background process and delivery.
				readCtx, stopRead := context.WithTimeout(run, 20*time.Second)
				_, err := m.waitThread(readCtx)
				stopRead()
				if err != nil {
					return
				}
				result = m.Reconcile(run, d)
			}
		}
		if result.Outcome == app.Retry {
			result.RetryAfter = max(result.RetryAfter, time.Second*5)
		}
		if err = p.App.FinishDelivery(run, d, result); err != nil {
			return
		}
	}
}

// Deliver routes stop requests by the persisted message identity, never selection state.
func (p *BackgroundPool) Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var input app.InterruptTurn
	if json.Unmarshal(d.Payload, &input) != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid interrupt"}
	}
	conversation, err := p.App.MessageConversation(ctx, input.MessageID)
	if err != nil {
		return retry(err)
	}
	if conversation == "main" {
		return p.Front.Control().Deliver(ctx, d)
	}
	p.mu.Lock()
	m := p.active[conversation]
	p.mu.Unlock()
	if m != nil {
		return m.Control().Deliver(ctx, d)
	}
	return app.DeliveryResult{Outcome: app.Unknown, Reason: "background process is no longer attached; inspect persisted turn state"}
}
func (p *BackgroundPool) Reconcile(ctx context.Context, d app.Delivery) app.DeliveryResult {
	// Control reconciliation only reads the persisted message/turn identity;
	// it must never issue a second native interrupt, for any conversation.
	return p.Front.Control().Reconcile(ctx, d)
}

// UpgradeDrainStatus includes startup and teardown, not just active model turns.
// Admission checks the durable upgrade gate while holding the same mutex.
func (p *BackgroundPool) UpgradeDrainStatus(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !p.mu.TryLock() {
		return false, nil
	}
	defer p.mu.Unlock()
	return len(p.active) == 0, nil
}
