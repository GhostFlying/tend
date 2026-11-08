package mainthread

import (
	"context"
	"errors"
)

// UpgradeDrainStatus is called after the durable gate is established. Holding
// workMu proves no delivery or tool callback is still executing.
// Unconfirmed native operations must settle before process replacement.
func (m *Manager) UpgradeDrainStatus(ctx context.Context) (bool, error) {
	if _, err := m.app.UpgradePausesMain(ctx); err != nil {
		return false, err
	}
	if !m.workMu.TryLock() {
		return false, nil
	}
	defer m.workMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session != nil && m.thread != nil && m.active == nil && !m.maintenanceBlocked, nil
}

// ArchiveDrainStatus observes external operation lifecycle rather than model
// readiness. A failed startup with no native work is not a drain blocker.
func (m *Manager) ArchiveDrainStatus(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !m.workMu.TryLock() {
		return false, nil
	}
	defer m.workMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.archiveStarting && !m.archiveTeardown && m.active == nil && !m.maintenanceBlocked, nil
}

// ArchiveDrainStatus includes receipt-only process startup and teardown.
func (p *BackgroundPool) ArchiveDrainStatus(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !p.mu.TryLock() {
		return false, nil
	}
	defer p.mu.Unlock()
	return len(p.active) == 0, nil
}

// RecoveryReadiness checks a usable initialized session rather than the idle
// conditions used for initial upgrade authorization. It never admits work.
func (m *Manager) RecoveryReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pending, err := m.app.ReceiptRecoveryStartupPending(ctx); err != nil {
		return err
	} else if pending {
		drained, err := m.ArchiveDrainStatus(ctx)
		if err != nil {
			return err
		}
		m.mu.Lock()
		unopened := m.session == nil && m.thread == nil
		m.mu.Unlock()
		if !drained || !unopened {
			return errors.New("receipt recovery startup has attached native work")
		}
		return nil
	}
	prepared, err := m.app.ArchivePreparation(ctx)
	if err != nil {
		return err
	}
	if prepared.Prepared {
		needsRead, err := m.app.ArchiveNeedsReceiptRead(ctx, m.cfg.ConversationID)
		if err != nil {
			return err
		}
		if !needsRead {
			drained, err := m.ArchiveDrainStatus(ctx)
			if err != nil {
				return err
			}
			if !drained {
				return errors.New("prepared main thread still has native work or teardown")
			}
			return nil
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil || m.thread == nil || m.archiveStarting || m.archiveTeardown || m.maintenanceBlocked {
		return errors.New("main thread startup or reconciliation is incomplete")
	}
	return nil
}
func (p *BackgroundPool) RecoveryReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		return errors.New("background pool not initialized")
	}
	if pending, err := p.App.ReceiptRecoveryStartupPending(ctx); err != nil {
		return err
	} else if pending {
		if len(p.active) != 0 {
			return errors.New("receipt recovery background startup has active native managers")
		}
		return nil
	}
	for _, m := range p.active {
		if err := m.RecoveryReadiness(ctx); err != nil {
			return err
		}
	}
	return nil
}
