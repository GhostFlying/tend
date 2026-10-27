package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"time"
)

func (m *Manager) refreshUsage(ctx context.Context, th harness.Thread) {
	if native, ok := th.(harness.ContextManager); ok {
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		usage, err := native.ContextUsage(query)
		if err == nil {
			if err = m.app.RecordAgentUsage(ctx, m.cfg.ConversationID, usage); err != nil {
				m.cfg.Log.WarnContext(ctx, "context usage persistence failed", "err", err)
			}
		}
	}
}
func (m *Manager) reconcileCompact(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p struct {
		ID string `json:"operation_id"`
	}
	if json.Unmarshal(d.Payload, &p) != nil {
		return app.DeliveryResult{Outcome: app.Failed}
	}
	if _, err := m.app.ReconcileCompactEvidence(ctx, p.ID); err != nil {
		return retry(err)
	}
	state, err := m.app.CompactState(ctx, p.ID)
	if err != nil {
		return retry(err)
	}
	if state == "completed" || state == "failed" {
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	if state == "queued" {
		return app.DeliveryResult{Outcome: app.Retry}
	}
	return app.DeliveryResult{Outcome: app.Unknown, Reason: "native compaction completion is not confirmed"}
}
func (m *Manager) deliverCompact(ctx context.Context, d app.Delivery) app.DeliveryResult {
	m.workMu.Lock()
	defer m.workMu.Unlock()
	if paused, err := m.app.UpgradePausesMain(ctx); err != nil {
		return retry(err)
	} else if paused {
		return retry(errors.New("mainthread: waiting for server upgrade"))
	}
	var p struct {
		ID string `json:"operation_id"`
	}
	if json.Unmarshal(d.Payload, &p) != nil || p.ID == "" {
		return app.DeliveryResult{Outcome: app.Failed}
	}
	state, err := m.app.CompactState(ctx, p.ID)
	if err != nil {
		return retry(err)
	}
	if state != "queued" {
		return m.reconcileCompact(ctx, d)
	}
	th, err := m.waitThread(ctx)
	if err != nil {
		return retry(err)
	}
	native, ok := th.(harness.ContextManager)
	if !ok {
		_ = m.app.SetCompactState(ctx, p.ID, th.ID(), "failed")
		return app.DeliveryResult{Outcome: app.Failed, Reason: "native compaction is unsupported"}
	}
	if err = m.app.SetCompactState(ctx, p.ID, th.ID(), "starting"); err != nil {
		return retry(err)
	}
	request, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = native.Compact(request)
	cancel()
	if err != nil {
		status := "unknown"
		outcome := app.Unknown
		if errors.Is(err, harness.ErrRejected) {
			status = "failed"
			outcome = app.Failed
		}
		_ = m.app.SetCompactState(ctx, p.ID, th.ID(), status)
		return app.DeliveryResult{Outcome: outcome, Reason: "native compact request did not complete"}
	}
	if err = m.app.SetCompactState(ctx, p.ID, th.ID(), "running"); err != nil {
		return app.DeliveryResult{Outcome: app.Unknown}
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return app.DeliveryResult{Outcome: app.Unknown}
		case <-timeout.C:
			_ = m.app.SetCompactState(ctx, p.ID, th.ID(), "unknown")
			return app.DeliveryResult{Outcome: app.Unknown, Reason: "waiting for native compaction completion"}
		case <-ticker.C:
			state, err = m.app.CompactState(ctx, p.ID)
			if err != nil {
				return app.DeliveryResult{Outcome: app.Unknown}
			}
			if state == "completed" {
				return app.DeliveryResult{Outcome: app.Delivered}
			}
		}
	}
}
