package mainthread

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (m *Manager) deliverRecovery(ctx context.Context, d app.Delivery) app.DeliveryResult {
	m.workMu.Lock()
	defer m.workMu.Unlock()
	r, ok, err := m.app.AgentRecovery(ctx, d.RequestID)
	if err != nil {
		return retry(err)
	}
	if !ok || r.ActiveRequestID != d.RequestID || r.Status == "cancelled" || r.Status == "completed" || r.Status == "blocked" || r.Status == "exhausted" {
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	rec, exists, err := m.app.MainTurn(ctx, d.RequestID)
	if err != nil {
		return retry(err)
	}
	if exists && rec.Status != store.TurnRejected {
		if rec.Status == store.TurnStarting || rec.Status == store.TurnStarted {
			if err := m.app.AwaitNativeReceipts(ctx, rec.ThreadID); err != nil {
				return retry(err)
			}
		}
		return m.Reconcile(ctx, d)
	}
	if r.Status == "starting" && (!exists || rec.Status == store.TurnRejected) {
		// No row proves no start call; a persisted rejection proves it did not start.
		if err := m.app.RequeueRejectedAgentRecovery(ctx, d.RequestID); err != nil {
			return retry(err)
		}
		return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second}
	}
	if r.Status != "waiting" {
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "Recovery startup requires native receipt reconciliation."}
	}
	if paused, err := m.app.UpgradePausesMain(ctx); err != nil {
		return retry(err)
	} else if paused {
		return retry(errors.New("mainthread: waiting for server upgrade"))
	}
	if blocked, err := m.app.AgentCompactUncertain(ctx, m.cfg.ConversationID); err != nil {
		return retry(err)
	} else if blocked {
		return retry(errors.New("native compact must be reconciled before recovery"))
	}
	allowed, err := m.app.CheckAgentRecoveryRead(ctx, d.RequestID, r.ThreadID)
	if err != nil {
		return retry(err)
	}
	if !allowed {
		fresh, _, err := m.app.AgentRecovery(ctx, d.RequestID)
		if err != nil {
			return retry(err)
		}
		if fresh.Status == "waiting" {
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second}
		}
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	readyCtx, stopReady := context.WithTimeout(ctx, 20*time.Second)
	th, err := m.waitThread(readyCtx)
	stopReady()
	if err != nil {
		return retry(err)
	}
	if th.ID() != r.ThreadID {
		_, err := m.app.AdmitAgentRecovery(ctx, d.RequestID, th.ID())
		if err != nil {
			return retry(err)
		}
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	reader, supported := th.(harness.TurnReader)
	if !supported {
		return m.blockRecovery(ctx, d, "Native executor cannot verify retained execution history.")
	}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	receipt, found, err := reader.FindTurn(readCtx, "", r.LastTurnID)
	cancel()
	if err != nil || !found || receipt.Status != "failed" || !receipt.ContinuationSafe {
		// No startup intent exists yet: retry only this read, not native input.
		return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second, Reason: "Waiting for a confirmed failed predecessor with settled native operations."}
	}
	openedSelection := m.namedSelection()
	selection, revision, err := m.turnModel(ctx, th, d.RequestID)
	if err != nil {
		if protocol.CodeOf(err) == protocol.CodeInvalidRequest {
			return m.rejectRecovery(ctx, d, "The selected model is unavailable; review conversation settings.")
		}
		return retry(err)
	}
	admitted, err := m.app.AdmitAgentRecovery(ctx, d.RequestID, th.ID(), revision)
	if err != nil {
		return retry(err)
	}
	if !admitted {
		fresh, _, err := m.app.AgentRecovery(ctx, d.RequestID)
		if err != nil {
			return retry(err)
		}
		if fresh.Status == "waiting" {
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second}
		}
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	msg, err := m.app.Message(ctx, r.MessageID)
	if err != nil {
		return retry(err)
	}
	a := &turn{requestID: d.RequestID, messageID: r.MessageID, sourceMessageID: r.MessageID, sourceRequestID: d.RequestID, sourceSeq: msg.Seq, sources: []string{r.MessageID}, done: make(chan string, 1), recovery: true}
	focus, sources, seq, err := m.retainedRecoverySources(ctx, d.RequestID, r.MessageID)
	if err != nil {
		return m.rejectRecovery(ctx, d, "Could not restore the retained owner input sources.")
	}
	a.sourceMessageID, a.sources, a.sourceSeq = focus, sources, seq
	m.mu.Lock()
	m.active = a
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.active == a {
			m.active = nil
		}
		m.mu.Unlock()
	}()
	rec = store.MainTurn{RequestID: d.RequestID, MessageID: r.MessageID, ThreadID: th.ID(), Status: store.TurnStarting, Model: selection.Model, ModelProvider: selection.Provider, Effort: selection.Effort, SettingsRevision: revision}
	// Freeze the actual executor opened by serve; PutMainTurn compares this
	// pin with current admission in the same transaction.
	rec.Hosted = openedSelection
	if err := m.app.PutMainTurn(ctx, rec); err != nil {
		if errors.Is(err, app.ErrRepairCancelled) {
			return app.DeliveryResult{Outcome: app.Delivered}
		}
		return retry(err)
	}
	// Refresh current ledger facts through the normal developer-context boundary.
	if m.cfg.ConversationID != "" && m.cfg.ConversationID != "main" {
		text, err := m.app.AgentContext(ctx, m.cfg.ConversationID, d.RequestID)
		if err != nil {
			return m.rejectRecovery(ctx, d, "Could not refresh the work-item context before continuation.")
		}
		if err := th.InjectDeveloper(ctx, text); err != nil {
			if errors.Is(err, harness.ErrRejected) {
				return m.rejectRecovery(ctx, d, "Native work-item context injection was rejected before continuation.")
			}
			return m.blockRecovery(ctx, d, "Native work-item context injection requires reconciliation.")
		}
	} else {
		packet, err := m.context.Prepare(ctx, m.threadKey(th.ID()), a.sourceSeq)
		if err != nil {
			return m.rejectRecovery(ctx, d, "Could not prepare current ledger context.")
		}
		if packet != nil {
			if err := th.InjectDeveloper(ctx, packet.Text); err != nil {
				_ = m.context.Fail(ctx, packet.ID, !errors.Is(err, harness.ErrRejected))
				if errors.Is(err, harness.ErrRejected) {
					return m.rejectRecovery(ctx, d, "Native ledger context injection was rejected before continuation.")
				}
				return m.blockRecovery(ctx, d, "Native ledger context injection requires reconciliation.")
			}
			if err := m.context.Confirm(ctx, packet.ID); err != nil {
				return m.blockRecovery(ctx, d, "Could not confirm current ledger context.")
			}
		}
	}
	allowed, err = m.app.ConfirmAgentRecoveryStart(ctx, d.RequestID, th.ID(), revision)
	if err != nil {
		return retry(err)
	}
	if !allowed {
		if err := m.app.RequeueRejectedAgentRecovery(ctx, d.RequestID); err != nil {
			return retry(err)
		}
		fresh, _, err := m.app.AgentRecovery(ctx, d.RequestID)
		if err != nil {
			return retry(err)
		}
		if fresh.Status == "waiting" {
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second}
		}
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	// Empty input lets the harness continue its retained history and tool outputs.
	id, err := th.StartTurn(ctx, harness.TurnInput{Continue: true, Model: selection.Model, ModelProvider: selection.Provider, Effort: selection.Effort})
	if err != nil {
		if errors.Is(err, harness.ErrRejected) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			rec.Status = store.TurnRejected
			if err := m.app.PutMainTurn(cleanup, rec); err != nil {
				return retry(err)
			}
			if err := m.app.RequeueRejectedAgentRecovery(cleanup, d.RequestID); err != nil {
				return retry(err)
			}
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second}
		}
		if s := m.detach(th); s != nil {
			_ = s.Close()
		}
		return m.blockRecovery(context.WithoutCancel(ctx), d, "Native continuation start did not answer; automatic resend is disabled.")
	}
	rec.TurnID, rec.Status = id, store.TurnStarted
	m.mu.Lock()
	a.turnID = id
	if status, ok := m.settled[id]; ok {
		delete(m.settled, id)
		a.done <- status
	}
	m.mu.Unlock()
	if err := m.app.PutMainTurn(ctx, rec); err != nil {
		return m.blockRecovery(ctx, d, "Could not persist the native continuation receipt.")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case status := <-a.done:
			rec.Status = finalStatus(status)
			break wait
		case <-ctx.Done():
			return app.DeliveryResult{Outcome: app.Delivered}
		case <-ticker.C:
			if err := m.steerOne(ctx, th, a); err != nil {
				m.cfg.Log.ErrorContext(ctx, "recovery steer failed", "err", err)
				m.blockSteer(a)
			}
		}
	}
	m.mu.Lock()
	rec.Failure = a.failure
	m.mu.Unlock()
	if err := m.app.SettleMainSteers(context.WithoutCancel(ctx), th.ID(), id, rec.Status); err != nil {
		return m.deferRecoveryReceipt(ctx, th, err)
	}
	if err := m.app.PutMainTurn(context.WithoutCancel(ctx), rec); err != nil {
		return m.deferRecoveryReceipt(ctx, th, err)
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}

func (m *Manager) blockRecovery(ctx context.Context, d app.Delivery, reason string) app.DeliveryResult {
	if err := m.app.BlockAgentRecovery(ctx, d.RequestID, reason); err != nil {
		return retry(err)
	}
	return app.DeliveryResult{Outcome: app.Unknown, Reason: reason}
}

func (m *Manager) captureFailure(a *turn) *protocol.AgentFailure {
	m.mu.Lock()
	defer m.mu.Unlock()
	return a.failure
}

func (m *Manager) rejectRecovery(ctx context.Context, d app.Delivery, reason string) app.DeliveryResult {
	if err := m.app.RejectAgentRecovery(ctx, d.RequestID, reason); err != nil {
		return retry(err)
	}
	return app.DeliveryResult{Outcome: app.Failed, Reason: reason}
}

// retainedRecoverySources reconstructs only inputs consumed in predecessor native
// turns. Current-turn user items are replayed in receipt order separately.
func (m *Manager) retainedRecoverySources(ctx context.Context, request, message string) (string, []string, int64, error) {
	original, err := m.app.Message(ctx, message)
	if err != nil {
		return "", nil, 0, err
	}
	messages := map[string]int64{message: original.Seq}
	for cursor := request; cursor != ""; {
		source, err := m.app.RecoverySourceRequest(ctx, cursor)
		if err != nil {
			return "", nil, 0, err
		}
		if source == "" {
			break
		}
		previous, found, err := m.app.MainTurn(ctx, source)
		if err != nil {
			return "", nil, 0, err
		}
		if !found {
			return "", nil, 0, errors.New("recovery predecessor execution is missing")
		}
		inputs, err := m.app.ConsumedMainSteers(ctx, previous.ThreadID, previous.TurnID)
		if err != nil {
			return "", nil, 0, err
		}
		for _, input := range inputs {
			owner, err := m.app.Message(ctx, input.MessageID)
			if err != nil {
				return "", nil, 0, err
			}
			messages[owner.MessageID] = owner.Seq
		}
		cursor = source
	}
	sources := make([]string, 0, len(messages))
	for id := range messages {
		sources = append(sources, id)
	}
	sort.Slice(sources, func(i, j int) bool { return messages[sources[i]] < messages[sources[j]] })
	focus := sources[len(sources)-1]
	return focus, sources, messages[focus], nil
}

// A known native completion survives failed local settlement through the same
// read-only reconciliation path as a disconnected execution.
func (m *Manager) deferRecoveryReceipt(ctx context.Context, th harness.Thread, cause error) app.DeliveryResult {
	if err := m.app.AwaitNativeReceipts(context.WithoutCancel(ctx), th.ID()); err != nil {
		m.cfg.Log.ErrorContext(ctx, "could not defer recovery receipt", "err", err)
		if s := m.detach(th); s != nil {
			_ = s.Close()
		}
	}
	return retry(cause)
}
