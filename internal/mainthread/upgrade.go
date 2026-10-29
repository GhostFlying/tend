package mainthread

import "context"

// UpgradeDrainStatus is called after the durable gate is established. Holding
// workMu proves no delivery or tool callback is still executing; a private
// summary also has to finish before the harness process can be replaced.
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
	return m.session != nil && m.thread != nil && m.active == nil && !m.summaryActive && !m.maintenanceBlocked, nil
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
	return !m.archiveStarting && !m.archiveTeardown && m.active == nil && !m.summaryActive && !m.maintenanceBlocked, nil
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
