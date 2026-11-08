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
	App      *app.App
	Front    *Manager
	Limit    int
	New      func(string) (*Manager, error)
	mu       sync.Mutex
	active   map[string]*Manager
	remote   map[string]bool
	database map[string]bool
	wait     func(context.Context, <-chan struct{}, <-chan struct{}, time.Duration) bool
	scanned  func()
	finished *app.Broadcast
	retries  map[string]backgroundRetry
}

type backgroundRetry struct {
	at    time.Time
	delay time.Duration
}

func (p *BackgroundPool) Run(ctx context.Context) {
	limit := p.Limit
	if limit <= 0 {
		limit = 2
	}
	p.mu.Lock()
	p.active = map[string]*Manager{}
	p.remote = map[string]bool{}
	p.database = map[string]bool{}
	p.finished = app.NewBroadcast()
	p.retries = map[string]backgroundRetry{}
	p.mu.Unlock()
	var wg sync.WaitGroup
	defer wg.Wait()
	completed := p.finished
	failedDelay := time.Duration(0)
	wait := p.wait
	if wait == nil {
		wait = waitBackground
	}
	for ctx.Err() == nil {
		changed, finished := p.App.Changes().Wait(), completed.Wait()
		scannedAt := p.App.BackgroundNow()
		failed := false
		if pending, err := p.App.ReceiptRecoveryStartupPending(ctx); err != nil || pending {
			delay := time.Duration(0)
			if err != nil {
				delay = 2 * time.Second
			}
			if !wait(ctx, changed, finished, delay) {
				return
			}
			continue
		}
		if p.scanned != nil {
			p.scanned()
		}
		ids, err := p.App.PendingAgentConversations(ctx)
		failed = err != nil
		if err == nil {
			retained := make(map[string]bool, len(ids))
			for _, id := range ids {
				retained[id] = true
			}
			p.mu.Lock()
			for id := range p.retries {
				if !retained[id] && p.active[id] == nil {
					delete(p.retries, id)
				}
			}
			p.mu.Unlock()
			for _, id := range ids {
				p.mu.Lock()
				retry, exists := p.retries[id]
				deferred := p.App.BackgroundNow().Before(retry.at)
				if exists && !deferred {
					retry.at = time.Time{}
					p.retries[id] = retry
				}
				p.mu.Unlock()
				if deferred {
					continue
				}
				if handled, err := p.startDatabaseReplyReplay(ctx, id, &wg); err != nil || handled {
					failed = failed || err != nil
					continue
				}
				claimed, err := p.App.AgentConversationClaimed(ctx, id)
				if err != nil || !claimed {
					failed = failed || err != nil
					continue
				}
				selected, named, err := p.App.HostedRuntimeSelection(ctx, id)
				if err != nil || !named {
					failed = failed || err != nil
					continue
				}
				remote := named && selected.RunnerManaged
				p.mu.Lock()
				local := len(p.active) - len(p.remote)
				if (!remote && local >= limit) || p.active[id] != nil {
					p.mu.Unlock()
					continue
				}
				paused, err := p.App.UpgradePausesMain(ctx)
				if err != nil {
					p.deferRetryLocked(id)
					p.mu.Unlock()
					break
				}
				if paused {
					preparation, err := p.App.ArchivePreparation(ctx)
					needed, readErr := p.App.ArchiveNeedsReceiptRead(ctx, id)
					parkRead := false
					var parkErr error
					if named && selected.RunnerManaged && selected.ReceiptOnly {
						parkRead, parkErr = p.App.UpgradeParkReceiptRead(ctx, id, selected.RunnerRunID)
					}
					if err != nil || readErr != nil || parkErr != nil || !(preparation.Prepared && needed || parkRead) {
						p.mu.Unlock()
						continue
					}
				}
				m, err := p.New(id)
				if err != nil {
					p.deferRetryLocked(id)
					p.mu.Unlock()
					continue
				}
				p.active[id] = m
				if remote {
					p.remote[id] = true
				}
				p.mu.Unlock()
				wg.Go(func() {
					p.finishAttachment(id, p.drain(ctx, id, m))
				})
			}
		}
		delay, err := p.App.BackgroundDelay(ctx, scannedAt)
		p.mu.Lock()
		for _, retry := range p.retries {
			if retry.at.IsZero() {
				continue
			}
			if remaining := max(time.Millisecond, retry.at.Sub(p.App.BackgroundNow())); delay == 0 || remaining < delay {
				delay = remaining
			}
		}
		p.mu.Unlock()
		failed = failed || err != nil
		if failed {
			failedDelay = min(30*time.Second, max(2*time.Second, failedDelay*2))
			if delay == 0 || failedDelay < delay {
				delay = failedDelay
			}
		} else {
			failedDelay = 0
		}
		if !wait(ctx, changed, finished, delay) {
			return
		}
	}
}

func (p *BackgroundPool) deferRetryLocked(id string) {
	if p.retries == nil {
		p.retries = map[string]backgroundRetry{}
	}
	delay := min(30*time.Second, max(2*time.Second, p.retries[id].delay*2))
	p.retries[id] = backgroundRetry{at: p.App.BackgroundNow().Add(delay), delay: delay}
}

func (p *BackgroundPool) finishAttachment(id string, failed bool) {
	p.mu.Lock()
	delete(p.active, id)
	delete(p.remote, id)
	delete(p.database, id)
	if failed {
		p.deferRetryLocked(id)
	} else {
		delete(p.retries, id)
	}
	wake := p.finished
	p.mu.Unlock()
	if wake != nil {
		wake.Notify()
	}
}

func waitBackground(ctx context.Context, changed, completed <-chan struct{}, delay time.Duration) bool {
	var tick <-chan time.Time
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		tick = timer.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-changed:
	case <-completed:
	case <-tick:
	}
	return true
}
func (p *BackgroundPool) drain(ctx context.Context, id string, m *Manager) (failed bool) {
	receiptRead, err := p.App.DeferAgentReceiptRead(ctx, id)
	if err != nil {
		return true
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(run) }()
	phase := "context_cancelled"
	var poolErr error
	var request string
	var outcome any
	defer func() {
		failed = poolErr != nil || outcome == app.Unknown || outcome == app.Retry
		m.cfg.Log.InfoContext(run, "manager pool attachment ending", "instance", m.cfg.Instance,
			"conversation_id", id, "pool_exit_phase", phase, "pool_error", poolErr,
			"delivery_request_id", request, "delivery_outcome", outcome, "context_cause", context.Cause(run))
		cancel()
		<-done
	}()
	// Startup settles abandoned turns before reconciliation can acknowledge
	// their deliveries. Do not tear down an uninitialized recovery process.
	waitCtx := run
	stopWait := func() {}
	if receiptRead {
		waitCtx, stopWait = context.WithTimeout(run, 20*time.Second)
	}
	thread, err := m.waitStartup(waitCtx)
	stopWait()
	if err != nil {
		phase, poolErr = "startup_failed", err
		return
	}
	if thread == nil {
		phase = "startup_paused"
		// Preparation can commit after pool selection but before serve reads
		// its admission gate. There is no native execution or retry to defer.
		return
	}
	dest := "background_thread:" + id
	inflight, err := p.App.InFlightDeliveries(run)
	if err != nil {
		phase, poolErr = "inflight_read_failed", err
		return
	}
	for _, d := range inflight {
		if d.Destination == dest {
			request, outcome = d.RequestID, nil
			if err := p.App.FinishDelivery(run, d, m.Reconcile(run, d)); err != nil {
				phase, poolErr = "inflight_settlement_failed", err
				return
			}
		}
	}
	if selected := m.namedSelection(); selected != nil && selected.ReceiptOnly {
		phase = "receipt_only_finished"
		// Reconcile original delivery receipts above, but never start fresh
		// business input on the fenced executor.
		return
	}
	for run.Err() == nil {
		if paused, err := p.App.UpgradePausesMain(run); err != nil || paused {
			phase, poolErr = "admission_paused", err
			return
		}
		d, _, ok, err := p.App.NextDelivery(run, dest)
		if err != nil || !ok {
			phase, poolErr = "delivery_unavailable", err
			return
		}
		request, outcome = d.RequestID, nil
		result := m.Deliver(run, d)
		outcome = result.Outcome
		if d.Operation == app.OpRecoverAgent {
			pending, err := p.App.NativeReceiptPending(run, d.RequestID)
			if err != nil {
				phase, poolErr = "recovery_receipt_read_failed", err
				return
			}
			if pending {
				// Let the replacement session read the known native execution
				// before releasing this background process and delivery.
				readCtx, stopRead := context.WithTimeout(run, 20*time.Second)
				_, err := m.waitThread(readCtx)
				stopRead()
				if err != nil {
					phase, poolErr = "recovery_attachment_failed", err
					return
				}
				result = m.Reconcile(run, d)
			}
		}
		if result.Outcome == app.Retry {
			result.RetryAfter = max(result.RetryAfter, time.Second*5)
		}
		outcome = result.Outcome
		if err = p.App.FinishDelivery(run, d, result); err != nil {
			phase, poolErr = "delivery_settlement_failed", err
			return
		}
		if selected := m.namedSelection(); selected != nil && selected.RunnerRunID != "" {
			phase = "runner_delivery_returned"
			return
		}
	}
	return
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
	database := p.database[conversation]
	p.mu.Unlock()
	if m != nil && !database {
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
