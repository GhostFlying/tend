package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (m *Manager) openManagedThread(ctx context.Context, s harness.Session) (harness.Thread, error) {
	hash, err := currentToolsetHash()
	if err != nil {
		return nil, err
	}
	id, exists, err := m.app.MainThread(ctx, m.cfg.Instance)
	if err != nil {
		return nil, err
	}
	if !exists {
		th, err := s.StartThread(ctx, m.threadConfig())
		if err != nil {
			return nil, err
		}
		if _, err := m.app.BindMainThread(ctx, m.cfg.Instance, th.ID(), hash, rolloutPath(th)); err != nil {
			return nil, err
		}
		return th, nil
	}
	if err := m.app.SettleOpenTurns(ctx, id); err != nil {
		return nil, err
	}
	binding, known, err := m.app.MainThreadBinding(ctx, m.cfg.Instance)
	if err != nil {
		return nil, err
	}
	needsHandoff := !known || binding.ToolsetHash != hash
	// On a schema upgrade, old callbacks must not reach the new tool parser.
	m.mu.Lock()
	m.maintenanceBlocked = needsHandoff
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.maintenanceBlocked = false; m.mu.Unlock() }()
	th, err := s.ResumeThread(ctx, id, m.threadConfig())
	if err != nil {
		if !errors.Is(err, harness.ErrRejected) {
			return nil, err
		}
		m.cfg.Log.WarnContext(ctx, "native source unavailable; recovering from retained history references", "thread", id, "err", err)
		th = &historyReference{id: id, path: binding.RolloutPath}
		needsHandoff = true
		m.mu.Lock()
		m.maintenanceBlocked = true
		m.mu.Unlock()
	} else if err := m.app.RecordMainThreadLocation(ctx, m.cfg.Instance, id, rolloutPath(th)); err != nil {
		return nil, err
	}
	if !needsHandoff {
		return th, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = m.reconcileNativeReceipts(readCtx, th)
	cancel()
	if err != nil {
		return nil, err
	}
	pending, err := m.app.UncertainNativeTurns(ctx, th.ID())
	if err != nil {
		return nil, err
	}
	if len(pending) != 0 {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "native receipts must be reconciled before changing the thread")
	}
	return m.migrateThread(ctx, s, th, hash, "")
}

func (m *Manager) idleSummaries(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.idleSummary(ctx); err != nil && ctx.Err() == nil {
				m.cfg.Log.WarnContext(ctx, "could not prepare idle summary", "err", err)
			}
		}
	}
}

// idleSummary reserves a snapshot briefly. The model runs on an isolated fork
// after releasing workMu, allowing the live conversation to continue normally.
func (m *Manager) idleSummary(ctx context.Context) error {
	if !m.cfg.MaintenanceEnabled {
		return nil
	}
	if !m.workMu.TryLock() {
		return nil
	}
	locked := true
	defer func() {
		if locked {
			m.workMu.Unlock()
		}
	}()
	if paused, err := m.app.UpgradePausesMain(ctx); err != nil {
		return err
	} else if paused {
		return nil
	}
	settings, err := m.app.HandoffSettings(ctx)
	if err != nil {
		return err
	}
	minutes := m.cfg.SummaryIdleMinutes
	if settings.Revision > 0 {
		minutes = settings.IdleMinutes
	}
	if minutes == 0 {
		return nil
	}
	m.mu.Lock()
	s, th, active, last, summaryActive := m.session, m.thread, m.active, m.lastActivity, m.summaryActive
	m.mu.Unlock()
	if s == nil || th == nil || active != nil || summaryActive {
		return nil
	}
	snap, err := m.app.HandoffSnapshot(ctx, m.cfg.Instance)
	if err != nil {
		return err
	}
	if snap.ThreadID != th.ID() || snap.Pending || snap.Active || snap.MaxSeq == 0 {
		return nil
	}
	activity := time.UnixMilli(snap.LastActivityMillis)
	if last.After(activity) {
		activity = last
	}
	if m.cfg.Now().Sub(activity) < time.Duration(minutes)*time.Minute {
		return nil
	}
	summary, exists, err := m.app.HandoffSummary(ctx, m.cfg.Instance)
	if err != nil {
		return err
	}
	if exists && summaryCovers(summary, snap) {
		return nil
	}
	claimed, err := m.app.ClaimSummaryAttempt(ctx, snap, int64((15*time.Minute)/time.Millisecond))
	if err != nil || !claimed {
		return err
	}
	m.mu.Lock()
	m.summaryActive = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.summaryActive = false; m.mu.Unlock() }()
	attempt := snap
	success := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := m.app.FinishSummaryAttempt(cleanup, attempt, success); err != nil {
			m.cfg.Log.WarnContext(cleanup, "could not record idle summary attempt", "err", err)
		}
	}()
	input, prompt, err := m.summaryInput(ctx, th)
	if err != nil {
		return err
	}
	if !attempt.Covers(input.Snapshot) {
		claimed, err := m.reclaimSummaryInput(ctx, attempt, input.Snapshot)
		if err != nil || !claimed {
			return err
		}
		attempt = input.Snapshot
	}

	fork, forker, err := m.summaryFork(ctx, s, th)
	if err != nil {
		return err
	}
	m.workMu.Unlock()
	locked = false
	_, err = m.generateSummary(ctx, s, fork, forker, input, prompt)
	success = err == nil
	return err
}

func (m *Manager) reconcileHandoff(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p protocol.ConversationHandoffPayload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid handoff request"}
	}
	if p.PrepareOnly {
		summary, ok, err := m.app.HandoffSummary(ctx, m.cfg.Instance)
		if err != nil {
			return retry(err)
		}
		snap, err := m.app.HandoffSnapshot(ctx, m.cfg.Instance)
		if err != nil {
			return retry(err)
		}
		if ok && summaryCovers(summary, snap) {
			return app.DeliveryResult{Outcome: app.Delivered}
		}
		// A private fork changes neither owner messages nor business state. Its
		// process disappeared with the previous app-server, so it is safe to retry.
		return app.DeliveryResult{Outcome: app.Retry}
	}
	record, exists, err := m.app.GetHandoff(ctx, d.RequestID)
	if err != nil {
		return retry(err)
	}
	if !exists {
		pending, ok, err := m.app.PendingHandoff(ctx, m.cfg.Instance)
		if err != nil {
			return retry(err)
		}
		if ok && pending.ID != d.RequestID {
			return conflictingHandoff()
		}
		return app.DeliveryResult{Outcome: app.Retry}
	}
	switch record.Phase {
	case "committed":
		return app.DeliveryResult{Outcome: app.Delivered}
	case "prepared", "created", "injected":
		return app.DeliveryResult{Outcome: app.Retry}
	default:
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "handoff contains an unconfirmed harness request; original mapping is preserved"}
	}
}
