package mainthread

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
)

func (m *Manager) steerIssueOwner(ctx context.Context, th harness.Thread, a *turn) error {
	selected := m.namedSelection()
	if selected == nil || !selected.RunnerManaged || selected.RequireReceiptReader || selected.RunnerRunID == "" {
		return nil
	}
	m.mu.Lock()
	blocked := a.finished || a.steerBlocked || m.thread != th
	turnID := a.turnID
	m.mu.Unlock()
	if blocked || turnID == "" {
		return nil
	}

	native, supported := th.(harness.TurnSteerer)
	steer, err := m.app.ClaimIssueOwnerSteer(ctx, a.requestID, selected.RunnerRunID, th.ID(), turnID, supported)
	if err != nil || steer.MessageID == "" {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, m.cfg.SteerTimeout)
	err = native.SteerTurn(callCtx, turnID, harness.TurnInput{Text: steer.Text, UserMessageID: steer.MessageID})
	cancel()

	status, reason := "accepted", "waiting_safe_boundary"
	switch {
	case errors.Is(err, harness.ErrSteerUnsupported):
		status, reason = "unsupported", "executor_unsupported"
	case errors.Is(err, harness.ErrRejected):
		status, reason = "rejected", "native_rejected"
	case err != nil:
		status, reason = "unknown", "native_outcome_unknown"
	}
	return m.app.RecordIssueOwnerSteerAnswer(context.WithoutCancel(ctx), steer.MessageID, status, reason)
}
