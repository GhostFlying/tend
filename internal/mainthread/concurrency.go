package mainthread

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// concurrencyForker releases a reservation only after native release succeeds.
// It does not wrap the Thread, preserving optional reconciliation interfaces.
type concurrencyForker struct {
	harness.ThreadForker
	app    *app.App
	permit string
	owner  *concurrencyOwner
}

func (f concurrencyForker) ReleaseThread(ctx context.Context, thread string) error {
	if err := f.ThreadForker.ReleaseThread(ctx, thread); err != nil {
		return err
	}
	return f.app.ObserveAuxiliaryConcurrency(ctx, f.permit, protocol.ConcurrencyFeedback{SessionID: thread}, true)
}

func (m *Manager) startSummaryStage(ctx context.Context, s harness.Session, cfg harness.ThreadConfig, forker harness.ThreadForker) (harness.Thread, harness.ThreadForker, error) {
	var owner *concurrencyOwner
	if prior, ok := forker.(concurrencyForker); ok {
		owner = prior.owner
		forker = prior.ThreadForker
	}
	permit, owner, err := m.reserveAuxiliary(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	thread, err := s.StartThread(ctx, cfg)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		stopErr := m.app.ObserveAuxiliaryConcurrency(cleanup, permit, protocol.ConcurrencyFeedback{}, errors.Is(err, harness.ErrRejected))
		return nil, nil, errors.Join(err, stopErr)
	}
	if permit != "" {
		forker = concurrencyForker{ThreadForker: forker, app: m.app, permit: permit, owner: owner}
	}
	return thread, forker, nil
}

type concurrencyOwner struct {
	closed  bool
	permits map[string]struct{}
}

func (m *Manager) reserveAuxiliary(ctx context.Context, owner *concurrencyOwner) (string, *concurrencyOwner, error) {
	m.mu.Lock()
	if owner == nil {
		owner = m.auxiliaryOwner
		if owner == nil {
			owner = &concurrencyOwner{permits: map[string]struct{}{}}
			m.auxiliaryOwner = owner
		}
	}
	closing := owner.closed
	m.mu.Unlock()
	if closing {
		return "", owner, harness.ErrRejected
	}
	id, err := m.app.AcquireAuxiliaryConcurrency(ctx, m.cfg.ConversationID, m.namedSelection())
	if err != nil || id == "" {
		return id, owner, err
	}
	m.mu.Lock()
	owner.permits[id] = struct{}{}
	closing = owner.closed
	m.mu.Unlock()
	if closing {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := m.app.StopAuxiliaryConcurrency(cleanup, []string{id})
		return "", owner, errors.Join(harness.ErrRejected, err)
	}
	return id, owner, nil
}
