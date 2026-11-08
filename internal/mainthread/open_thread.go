package mainthread

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (m *Manager) openManagedThread(ctx context.Context, s harness.Session) (harness.Thread, error) {
	hash, err := m.currentToolsetHash()
	if err != nil {
		return nil, err
	}
	id, exists, err := m.app.MainThread(ctx, m.bindingInstance())
	if err != nil {
		return nil, err
	}
	if !exists {
		th, err := s.StartThread(ctx, m.threadConfig())
		if err != nil {
			return nil, err
		}
		if _, err := m.app.BindMainThread(ctx, m.bindingInstance(), th.ID(), hash, rolloutPath(th)); err != nil {
			return nil, err
		}
		return th, nil
	}
	if err := m.app.SettleOpenTurns(ctx, id); err != nil {
		return nil, err
	}
	binding, known, err := m.app.MainThreadBinding(ctx, m.bindingInstance())
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
	} else if path := rolloutPath(th); path != "" {
		// Native history locations are optional (including runner-hosted
		// coordination). An unavailable index neither blocks a valid resume
		// nor erases the original index already retained in the binding.
		if err := m.app.RecordMainThreadLocation(ctx, m.bindingInstance(), id, path); err != nil {
			return nil, err
		}
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
