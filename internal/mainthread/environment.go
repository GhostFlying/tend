package mainthread

import (
	"context"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type capabilityEnvironmentSession interface {
	EnvironmentPending() bool
	EnvironmentQuiescent(context.Context, string) (bool, error)
	AdoptEnvironment(context.Context) error
}

func environmentResumeOnly(s harness.Session) bool {
	adoption, ok := s.(interface{ EnvironmentRequiresOriginalThread() bool })
	return ok && adoption.EnvironmentRequiresOriginalThread()
}

func (m *Manager) resumeConsultationEnvironment(ctx context.Context, s harness.Session) (harness.Thread, error) {
	instance := m.bindingInstance()
	id, exists, err := m.app.MainThread(ctx, instance)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "original consultation thread is unavailable")
	}
	installed, err := m.app.MainThreadToolset(ctx, instance, id)
	if err != nil {
		return nil, err
	}
	current, err := m.currentToolsetHash()
	if err != nil {
		return nil, err
	}
	if installed != current {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "consultation toolset changed; scoped maintenance is required before environment adoption")
	}
	// Consultation does not own Main's context or history. It cannot enter the
	// Main/Issue handoff path, even when no enriched binding hash was stored.
	return s.ResumeThread(ctx, id, m.threadConfig())
}

// environmentRestart serializes replacement with deliveries, maintenance and
// recovery; it does not create an input, tool callback or business retry.
func (m *Manager) environmentRestart(ctx context.Context, s harness.Session, th harness.Thread) (bool, error) {
	env, ok := s.(capabilityEnvironmentSession)
	if !ok || !env.EnvironmentPending() {
		return false, nil
	}
	if !m.workMu.TryLock() {
		return false, nil
	}
	defer m.workMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil || m.maintenanceBlocked || m.archiveStarting || m.archiveTeardown || m.compactRecovery || m.session != s {
		return false, nil
	}
	pending, err := m.app.UncertainNativeTurns(ctx, th.ID())
	if err != nil || len(pending) > 0 {
		return false, err
	}
	if blocked, err := m.app.AgentRecoveryUncertain(ctx, m.cfg.ConversationID); err != nil || blocked {
		return false, err
	}
	if blocked, err := m.app.AgentCompactUncertain(ctx, m.cfg.ConversationID); err != nil || blocked {
		return false, err
	}
	idle, err := env.EnvironmentQuiescent(ctx, th.ID())
	if err != nil || !idle {
		return false, err
	}
	if err = env.AdoptEnvironment(ctx); err != nil {
		return false, err
	}
	// Reserve the teardown before releasing delivery admission. serve's normal
	// defer detaches the native process and ordinary startup resumes the thread.
	m.session, m.thread = nil, nil
	m.ready = make(chan struct{})
	return true, nil
}

// restartFrozenEnvironment is called with delivery admission held. Startup
// restores the serviceable request's retained composition, never a new input.
func (m *Manager) restartFrozenEnvironment(ctx context.Context, s harness.Session, th harness.Thread, request string) error {
	env, ok := s.(capabilityEnvironmentSession)
	if !ok {
		return nil
	}
	m.mu.Lock()
	blocked := m.active != nil || m.maintenanceBlocked || m.archiveStarting || m.archiveTeardown || m.compactRecovery || m.session != s
	m.mu.Unlock()
	if blocked {
		return nil
	}
	pending, err := m.app.UncertainNativeTurns(ctx, th.ID())
	if err != nil || len(pending) > 0 {
		return err
	}
	if blocked, err := m.app.AgentRecoveryUncertain(ctx, m.cfg.ConversationID); err != nil || blocked {
		return err
	}
	if blocked, err := m.app.AgentCompactUncertain(ctx, m.cfg.ConversationID); err != nil || blocked {
		return err
	}
	idle, err := env.EnvironmentQuiescent(ctx, th.ID())
	if err != nil || !idle {
		return err
	}
	if detached := m.detach(th); detached != nil {
		return m.closeSession(ctx, detached, request, "restore_frozen_environment")
	}
	return nil
}
