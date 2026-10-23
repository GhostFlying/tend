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
