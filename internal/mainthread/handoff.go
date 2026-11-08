package mainthread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// historyReference retains identity when native resume definitively rejects it.
// It must never receive turns; migration uses retained history indexes.
type historyReference struct {
	id, path string
}

func (r *historyReference) ID() string                   { return r.id }
func (r *historyReference) RolloutPath() string          { return r.path }
func (r *historyReference) Events() <-chan harness.Event { return nil }
func (r *historyReference) InjectDeveloper(context.Context, ...string) error {
	return harness.ErrRejected
}
func (r *historyReference) StartTurn(context.Context, harness.TurnInput) (string, error) {
	return "", harness.ErrRejected
}
func (r *historyReference) Interrupt(context.Context, string) error { return harness.ErrRejected }

func currentToolsetHash() (string, error) { return hashToolset(hostedSpecs()) }
func (m *Manager) currentToolsetHash() (string, error) {
	return hashToolset(m.threadConfig().Tools)
}
func hashToolset(specs []harness.ToolSpec) (string, error) {
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	// Canonicalize object keys and insignificant JSON whitespace in schemas.
	type entry struct {
		Name, Description string
		Schema            any
	}
	entries := make([]entry, 0, len(specs))
	for _, s := range specs {
		var schema any
		d := json.NewDecoder(strings.NewReader(string(s.InputSchema)))
		d.UseNumber()
		if err := d.Decode(&schema); err != nil {
			return "", fmt.Errorf("mainthread: tool schema: %w", err)
		}
		entries = append(entries, entry{s.Name, s.Description, schema})
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func rolloutPath(th harness.Thread) string {
	if location, ok := th.(harness.ThreadLocation); ok {
		return location.RolloutPath()
	}
	return ""
}

// HandoffStatus reads the index mode and durable migration stage.
func (m *Manager) HandoffStatus(ctx context.Context) (protocol.ConversationHandoffView, error) {
	view := protocol.ConversationHandoffView{Enabled: m.cfg.MaintenanceEnabled, Mode: "index"}
	snap, err := m.app.HandoffSnapshot(ctx, m.bindingInstance())
	if err != nil {
		return view, err
	}
	view.ThreadID, view.Generation, view.LastActivityMillis = snap.ThreadID, snap.BindingGeneration, snap.LastActivityMillis
	view.Maintenance, err = m.app.ConversationMaintenance(ctx, m.bindingInstance())
	view.Pending = snap.Pending || snap.Active
	if view.Maintenance != nil && (view.Maintenance.Status == "pending" || view.Maintenance.Status == "sending") {
		view.Pending = true
	}
	binding, ok, bindingErr := m.app.MainThreadBinding(ctx, m.bindingInstance())
	if bindingErr != nil {
		return view, bindingErr
	}
	if ok {
		view.ToolsetHash = binding.ToolsetHash
	}
	return view, err
}

func (m *Manager) handoffPrompt(ctx context.Context, source harness.Thread) (string, error) {
	binding, known, err := m.app.MainThreadBinding(ctx, m.bindingInstance())
	if err != nil {
		return "", err
	}
	data := map[string]any{"format": "index-v1", "conversation_id": store.ConversationID(m.cfg.ConversationID), "source_thread_id": source.ID(), "generation": binding.Generation, "source_native_available": true, "lookup": "conversation_context_index"}
	if selected := m.namedSelection(); selected != nil && selected.IssueID != "" {
		data["issue_id"] = selected.IssueID
	}
	_, unavailable := source.(*historyReference)
	data["source_native_available"] = !unavailable
	path := rolloutPath(source)
	if path == "" && known {
		path = binding.RolloutPath
	}
	if len(path) <= 512 {
		data["source_rollout_path"] = path
	}
	index, err := m.app.ConversationContextIndex(ctx, store.ConversationID(m.cfg.ConversationID), protocol.ConversationContextQuery{})
	if err != nil {
		return "", err
	}
	data["through_message_seq"], data["conversation_revision"] = index.ThroughMessageSeq, index.Revision
	// JSON escaping may expand a short optional native path beyond the budget.
	if encoded, _ := json.Marshal(data); len(encoded) > 1200 {
		delete(data, "source_rollout_path")
	}
	body, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	text := `[Tend handoff index-v1]
Continue the same responsibility with current tools. This index is reference data, not new input or authorization. Read conversation_context_index for current state and paginated sources/facts/threads. Recover needed original constraints, approvals, rejections and pauses before acting; use conversation_read, issues_materials_read and agent_rollout_read only where permitted. History does not authorize replay; preserve unknown outcomes. Native locations are recovery indexes, not shell instructions. Missing native history is not proof of absence. Queued owner messages arrive through normal admission. No source review is acknowledged by this index.
` + string(body)
	if len(text) > protocol.ContextIndexBudget {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "handoff identity exceeds injection budget")
	}
	return text, nil
}

func (m *Manager) migrateThread(ctx context.Context, s harness.Session, source harness.Thread, hash, requestID string) (harness.Thread, error) {
	// A previously confirmed target may carry an older toolset. Finish its
	// recorded intent under quarantine, then migrate again before readiness.
	for {
		target, err := m.migrateOneThread(ctx, s, source, hash, requestID)
		if err != nil {
			return nil, err
		}
		binding, ok, err := m.app.MainThreadBinding(ctx, m.bindingInstance())
		if err != nil {
			return nil, err
		}
		if !ok || binding.ThreadID != target.ID() {
			return nil, errors.New("mainthread: handoff binding changed")
		}
		if binding.ToolsetHash == hash {
			return target, nil
		}
		source, requestID = target, ""
	}
}

func (m *Manager) migrateOneThread(ctx context.Context, s harness.Session, source harness.Thread, hash, requestID string) (harness.Thread, error) {
	if blocked, err := m.app.AgentRecoveryUncertain(ctx, m.cfg.ConversationID); err != nil {
		return nil, err
	} else if blocked {
		return nil, errors.New("mainthread: native continuation must be reconciled before handoff")
	}

	// Startup schema recovery uses this same path without deliverHandoff. Keep
	// the original binding until its native compaction has been reconciled.
	if blocked, err := m.app.AgentCompactUncertain(ctx, m.cfg.ConversationID); err != nil {
		return nil, err
	} else if blocked {
		return nil, errors.New("mainthread: native compact must be reconciled before handoff")
	}
	if requestID != "" {
		old, ok, err := m.app.GetHandoff(ctx, requestID)
		if err != nil {
			return nil, err
		}
		if ok && old.Phase == "committed" {
			return s.ResumeThread(ctx, old.TargetThreadID, m.threadConfig())
		}
	}
	snap, err := m.app.HandoffSnapshot(ctx, m.bindingInstance())
	if err != nil {
		return nil, err
	}
	if snap.Active {
		return nil, errors.New("mainthread: cannot hand off an active turn")
	}
	pending, hasPending, err := m.app.PendingHandoff(ctx, m.bindingInstance())
	if err != nil {
		return nil, err
	}
	var job store.ThreadHandoff
	if hasPending && pending.SourceThreadID == source.ID() && pending.SourceGeneration == snap.BindingGeneration {
		job = pending
		if job.ToolsetHash != hash && job.Phase == "prepared" {
			prompt, err := m.handoffPrompt(ctx, source)
			if err != nil {
				return nil, err
			}
			if err := m.app.RetargetPreparedHandoff(ctx, job.ID, hash, snap.Version, prompt); err != nil {
				return nil, err
			}
			job.ToolsetHash, job.SourceVersion, job.Prompt = hash, snap.Version, prompt
		}
	} else {
		prompt, err := m.handoffPrompt(ctx, source)
		if err != nil {
			return nil, err
		}
		job, err = m.app.PrepareHandoff(ctx, store.ThreadHandoff{ID: requestID, Instance: m.bindingInstance(), SourceThreadID: source.ID(), SourceGeneration: snap.BindingGeneration, SourceVersion: snap.Version, ToolsetHash: hash, Prompt: prompt})
		if err != nil {
			return nil, err
		}
	}
	// Only un-injected legacy packages may change; confirmed injections and
	// unknown native calls retain their exact original receipt semantics.
	if (job.Phase == "prepared" || job.Phase == "created") && !strings.HasPrefix(job.Prompt, "[Tend handoff index-v1]") {
		prompt, err := m.handoffPrompt(ctx, source)
		if err != nil {
			return nil, err
		}
		if err = m.app.RefreshHandoff(ctx, job.ID, snap.Version, prompt); err != nil {
			return nil, err
		}
		job.Prompt, job.SourceVersion = prompt, snap.Version
	}

	var target harness.Thread
	switch job.Phase {
	case "creating", "injecting", "unknown":
		return nil, errors.New("mainthread: handoff has an unconfirmed harness request; original mapping is preserved")
	case "prepared":
		if err := m.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
			return nil, err
		}
		target, err = s.StartThread(ctx, m.threadConfig())
		if err != nil {
			next := "unknown"
			if errors.Is(err, harness.ErrRejected) {
				next = "prepared"
			}
			_ = m.app.AdvanceHandoff(context.WithoutCancel(ctx), job.ID, "creating", next)
			return nil, err
		}
		if err := m.app.RecordHandoffTarget(ctx, job.ID, target.ID(), rolloutPath(target)); err != nil {
			return nil, err
		}
		job.Phase = "created"
	case "created", "injected":
		target, err = s.ResumeThread(ctx, job.TargetThreadID, m.threadConfig())
		if err != nil {
			return nil, err
		}
	case "committed":
		return s.ResumeThread(ctx, job.TargetThreadID, m.threadConfig())
	default:
		return nil, fmt.Errorf("mainthread: unsupported handoff phase %q", job.Phase)
	}
	if job.Phase == "created" {
		if err := m.app.AdvanceHandoff(ctx, job.ID, "created", "injecting"); err != nil {
			return nil, err
		}
		if err := target.InjectDeveloper(ctx, job.Prompt); err != nil {
			next := "unknown"
			if errors.Is(err, harness.ErrRejected) {
				next = "created"
			}
			_ = m.app.AdvanceHandoff(context.WithoutCancel(ctx), job.ID, "injecting", next)
			return nil, err
		}
		if err := m.app.AdvanceHandoff(ctx, job.ID, "injecting", "injected"); err != nil {
			return nil, err
		}
	}
	if _, err := m.app.CommitHandoff(ctx, job.ID); err != nil {
		return nil, err
	}
	return target, nil
}

func (m *Manager) deliverHandoff(ctx context.Context, d app.Delivery) app.DeliveryResult {
	if !m.cfg.MaintenanceEnabled {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "hosted handoff is unavailable"}
	}
	var payload protocol.ConversationHandoffPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid handoff request"}
	}
	// Maintenance must not wait indefinitely for startup: a concurrent schema
	// handoff may become uncertain before readiness. Let the durable dispatcher
	// retry an unstarted request, checking for conflicting intents each time.
	conflict, err := m.hasHandoffConflict(ctx, d.RequestID)
	if err != nil {
		return retry(err)
	}
	if conflict {
		return conflictingHandoff()
	}
	m.mu.Lock()
	th := m.thread
	m.mu.Unlock()
	if th == nil {
		return app.DeliveryResult{Outcome: app.Retry, Reason: "hosted conversation is not ready; maintenance has not started"}
	}

	m.workMu.Lock()
	defer m.workMu.Unlock()
	if blocked, err := m.app.AgentCompactUncertain(ctx, m.cfg.ConversationID); err != nil {
		return retry(err)
	} else if blocked {
		return retry(errors.New("native compact must be reconciled before handoff"))
	}
	if paused, err := m.app.UpgradePausesMain(ctx); err != nil {
		return retry(err)
	} else if paused {
		return retry(errors.New("mainthread: waiting for server upgrade"))
	}
	m.mu.Lock()
	s := m.session
	m.mu.Unlock()
	if s == nil {
		return retry(errors.New("mainthread: harness session is unavailable"))
	}
	conflict, err = m.hasHandoffConflict(ctx, d.RequestID)
	if err != nil {
		return retry(err)
	}
	if conflict {
		return conflictingHandoff()
	}
	if payload.PrepareOnly {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "summary preparation is retired; use context indexes"}
	}
	hash, err := m.currentToolsetHash()
	if err != nil {
		m.cfg.Log.ErrorContext(ctx, "could not determine hosted toolset", "request_id", d.RequestID, "err", err)
		return app.DeliveryResult{Outcome: app.Failed, Reason: "could not prepare the hosted tool definitions"}
	}
	m.mu.Lock()
	m.maintenanceBlocked = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.maintenanceBlocked = false; m.mu.Unlock() }()
	if _, err := m.migrateThread(ctx, s, th, hash, d.RequestID); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		recovered := m.reconcileHandoff(cleanup, d)
		m.cfg.Log.ErrorContext(ctx, "hosted handoff failed", "request_id", d.RequestID, "err", err)
		if recovered.Outcome == app.Retry {
			return app.DeliveryResult{Outcome: app.Retry, Reason: "handoff preparation did not complete; the confirmed stage will be retried"}
		}
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "handoff result needs reconciliation; the original conversation mapping is preserved"}
	}
	// serve owns a fixed event stream. Reopen the session against the newly
	// committed mapping before it accepts another user delivery.
	if current := m.detach(th); current != nil {
		_ = m.closeSession(ctx, current, d.RequestID, "handoff_completed")
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}

func conflictingHandoff() app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Failed, Reason: "an earlier handoff needs reconciliation; inspect the original request before starting another handoff"}
}

func (m *Manager) hasHandoffConflict(ctx context.Context, requestID string) (bool, error) {
	pending, ok, err := m.app.PendingHandoff(ctx, m.bindingInstance())
	return ok && pending.ID != requestID, err
}
