package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// HandoffSettings reads the independent shared owner override.
func (a *App) HandoffSettings(ctx context.Context) (v store.HandoffSettings, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error { v, err = store.GetHandoffSettings(ctx, tx.Q()); return err })
	return
}

// SetConversationHandoffSettings is independent of model catalog availability.
type SetConversationHandoffSettings struct{}

func (SetConversationHandoffSettings) Operation() string { return "conversation.set_handoff_settings" }
func (SetConversationHandoffSettings) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (SetConversationHandoffSettings) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "conversation" || x.Env.Target.ID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "target must be the shared conversation")
	}
	var p struct {
		IdleMinutes *int64 `json:"idle_minutes"`
	}
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.IdleMinutes == nil || *p.IdleMinutes < 0 || *p.IdleMinutes > 10080 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "idle_minutes must be between 0 and 10080")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	current, err := store.GetHandoffSettings(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if expected != current.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "handoff settings have changed; read them again")
	}
	next := store.HandoffSettings{IdleMinutes: *p.IdleMinutes, Revision: current.Revision + 1}
	if _, err := tx.Exec(ctx, `INSERT INTO conversation_handoff_settings VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET idle_minutes=excluded.idle_minutes,revision=excluded.revision,updated_at=excluded.updated_at`, next.IdleMinutes, next.Revision, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	result := map[string]any{"idle_minutes": next.IdleMinutes, "revision": next.Revision}
	return Outcome{Result: result, Events: []Event{{ObjectType: "conversation", ObjectID: "main", ObjectVersion: next.Revision, Kind: "conversation.handoff_settings", Body: result}}}, nil
}

// ConversationHandoff serializes owner handoffs with normal user deliveries.
type ConversationHandoff struct{}

func (ConversationHandoff) Operation() string { return "conversation.handoff" }
func (ConversationHandoff) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (ConversationHandoff) Apply(_ context.Context, x *Exec, _ *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "conversation" || x.Env.Target.ID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "target must be the shared conversation")
	}
	var p protocol.ConversationHandoffPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"queued": true, "prepare_only": p.PrepareOnly}, Events: []Event{{ObjectType: "conversation", ObjectID: "main", Kind: "conversation.handoff_requested", Body: map[string]any{"prepare_only": p.PrepareOnly}}}, Outbox: []OutboxMessage{{Destination: MainThreadDestination, Operation: "conversation.handoff", Payload: p}}}, nil
}

// MainThreadBinding reads enriched metadata while main_threads remains authority.
func (a *App) MainThreadBinding(ctx context.Context, instance string) (v store.MainThreadBinding, ok bool, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error { v, err = store.GetMainThreadBinding(ctx, tx.Q(), instance); return err })
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
		return
	}
	ok = err == nil
	return
}
func (a *App) HandoffSnapshot(ctx context.Context, instance string) (v store.HandoffSnapshot, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error { v, err = store.GetHandoffSnapshot(ctx, tx.Q(), instance); return err })
	return
}
func (a *App) HandoffSummary(ctx context.Context, instance string) (v store.HandoffSummary, ok bool, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		v, err = store.GetHandoffSummary(ctx, tx.Q(), instance)
		if err != nil {
			return err
		}
		var withdrawn bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_message_controls c JOIN conversation_changes d ON d.message_id=c.message_id WHERE c.withdrawn_at<>0 AND d.last_version>?)`, v.Snapshot.Version).Scan(&withdrawn); err != nil {
			return err
		}
		if withdrawn {
			return sql.ErrNoRows
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
		return
	}
	ok = err == nil
	return
}
func (a *App) GetHandoff(ctx context.Context, id string) (v store.ThreadHandoff, ok bool, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error { v, err = store.GetThreadHandoff(ctx, tx.Q(), id); return err })
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
		return
	}
	ok = err == nil
	return
}
func (a *App) PendingHandoff(ctx context.Context, instance string) (v store.ThreadHandoff, ok bool, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error { v, err = store.GetPendingHandoff(ctx, tx.Q(), instance); return err })
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
		return
	}
	ok = err == nil
	return
}

func (a *App) handoffWrite(ctx context.Context, f func(*store.Tx) error) error {
	err := a.store.Write(ctx, f)
	if err == nil {
		a.notifyChange()
	}
	return err
}
func (a *App) handoffEvent(ctx context.Context, tx *store.Tx, id, kind string) error {
	_, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "conversation", ObjectID: id, Kind: kind, BodyJSON: `{}`, CreatedAt: a.now().UnixMilli()})
	return err
}

// BindMainThread registers a newly created thread, never adopting a legacy hash.
func (a *App) BindMainThread(ctx context.Context, instance, threadID, toolsetHash, path string) (v store.MainThreadBinding, err error) {
	if instance == "" || threadID == "" || toolsetHash == "" {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "a new thread needs identity and toolset hash")
	}
	err = a.handoffWrite(ctx, func(tx *store.Tx) error {
		old, err := store.GetMainThreadBinding(ctx, tx.Q(), instance)
		if err == nil {
			if old.ThreadID == threadID && old.Generation > 0 && old.ToolsetHash == toolsetHash && old.RolloutPath == path {
				v = old
				return nil
			}
			return protocol.Errorf(protocol.CodeInvalidTransition, "existing native history requires a handoff")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		v = store.MainThreadBinding{Instance: instance, ThreadID: threadID, ToolsetHash: toolsetHash, RolloutPath: path, Generation: 1, Ancestors: []store.ThreadAncestor{}}
		if err := tx.PutMainThread(ctx, instance, threadID, a.now().UnixMilli()); err != nil {
			return err
		}
		return tx.PutMainThreadBinding(ctx, v, a.now().UnixMilli())
	})
	return
}

// SaveHandoffSummary retains even stale results without claiming newer coverage.
func (a *App) SaveHandoffSummary(ctx context.Context, v store.HandoffSummary) (store.HandoffSummary, error) {
	if v.Instance == "" || v.Snapshot.Instance != v.Instance || v.Snapshot.ThreadID == "" || v.FormatVersion < 1 || strings.TrimSpace(v.Body) == "" || v.Snapshot.Active || v.Snapshot.Pending {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "summary needs a quiescent source snapshot and nonempty body")
	}
	if len(v.Body) > 1<<20 {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "summary is too large")
	}
	if v.ID == "" {
		v.ID = a.ids.New(protocol.PrefixRequest)
	}
	v.CreatedAtMillis = a.now().UnixMilli()
	snapshot, err := json.Marshal(v.Snapshot)
	if err != nil {
		return v, err
	}
	err = a.handoffWrite(ctx, func(tx *store.Tx) error {
		inserted, err := tx.Exec(ctx, `INSERT INTO conversation_summaries(summary_id,instance,source_thread_id,source_version,snapshot_json,body,format_version,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(instance,source_thread_id,source_version,format_version) DO NOTHING`, v.ID, v.Instance, v.Snapshot.ThreadID, v.Snapshot.Version, string(snapshot), v.Body, v.FormatVersion, v.CreatedAtMillis)
		if err != nil {
			return err
		}
		// Return the immutable first result for this source snapshot.
		var savedSnapshot string
		err = tx.Q().QueryRowContext(ctx, `SELECT summary_id,snapshot_json,body,created_at FROM conversation_summaries WHERE instance=? AND source_thread_id=? AND source_version=? AND format_version=?`, v.Instance, v.Snapshot.ThreadID, v.Snapshot.Version, v.FormatVersion).Scan(&v.ID, &savedSnapshot, &v.Body, &v.CreatedAtMillis)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(savedSnapshot), &v.Snapshot); err != nil {
			return err
		}
		rows, err := inserted.RowsAffected()
		if err != nil || rows == 0 {
			return err
		}
		return a.handoffEvent(ctx, tx, v.ID, "conversation.summary")
	})
	return v, err
}

// PrepareHandoff persists external intent before native thread creation.
func (a *App) PrepareHandoff(ctx context.Context, v store.ThreadHandoff) (store.ThreadHandoff, error) {
	if v.Instance == "" || v.SourceThreadID == "" || v.ToolsetHash == "" || strings.TrimSpace(v.Prompt) == "" {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "handoff needs source, target tools and prompt")
	}
	if v.ID == "" {
		v.ID = a.ids.New(protocol.PrefixRequest)
	}
	err := a.handoffWrite(ctx, func(tx *store.Tx) error {
		old, err := store.GetPendingHandoff(ctx, tx.Q(), v.Instance)
		if err == nil {
			if old.SourceThreadID != v.SourceThreadID || old.SourceGeneration != v.SourceGeneration || old.ToolsetHash != v.ToolsetHash {
				return protocol.Errorf(protocol.CodeInvalidTransition, "an earlier handoff needs reconciliation")
			}
			v = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		binding, err := store.GetMainThreadBinding(ctx, tx.Q(), v.Instance)
		if err != nil {
			return err
		}
		if binding.ThreadID != v.SourceThreadID || binding.Generation != v.SourceGeneration {
			return protocol.Errorf(protocol.CodeStaleRevision, "main thread changed")
		}
		v.Phase = "prepared"
		v.CreatedAtMillis = a.now().UnixMilli()
		v.UpdatedAtMillis = v.CreatedAtMillis
		_, err = tx.Exec(ctx, `INSERT INTO main_thread_handoffs(handoff_id,instance,source_thread_id,source_generation,source_version,toolset_hash,prompt,phase,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Instance, v.SourceThreadID, v.SourceGeneration, v.SourceVersion, v.ToolsetHash, v.Prompt, v.Phase, v.CreatedAtMillis, v.UpdatedAtMillis)
		return err
	})
	return v, err
}

// AdvanceHandoff is a compare-and-swap over confirmed or uncertain phases.
func (a *App) AdvanceHandoff(ctx context.Context, id, from, to string) error {
	allowed := map[string]map[string]bool{"prepared": {"creating": true}, "creating": {"prepared": true, "unknown": true}, "created": {"injecting": true}, "injecting": {"created": true, "injected": true, "unknown": true}}
	if !allowed[from][to] {
		return protocol.Errorf(protocol.CodeInvalidTransition, "invalid handoff phase transition")
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		v, err := store.GetThreadHandoff(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if v.Phase == to {
			return nil
		}
		if v.Phase != from {
			return protocol.Errorf(protocol.CodeStaleRevision, "handoff phase changed")
		}
		_, err = tx.Exec(ctx, `UPDATE main_thread_handoffs SET phase=?,updated_at=? WHERE handoff_id=? AND phase=?`, to, a.now().UnixMilli(), id, from)
		return err
	})
}

// RecordHandoffTarget saves the confirmed create response before injection.
func (a *App) RecordHandoffTarget(ctx context.Context, id, threadID, path string) error {
	if threadID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "target native thread is required")
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		v, err := store.GetThreadHandoff(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if v.TargetThreadID != "" {
			if v.TargetThreadID == threadID && v.TargetRolloutPath == path {
				return nil
			}
			return protocol.Errorf(protocol.CodeConflictPayload, "handoff target already recorded")
		}
		if v.Phase != "creating" || v.SourceThreadID == threadID {
			return protocol.Errorf(protocol.CodeInvalidTransition, "handoff is not creating a fresh target")
		}
		_, err = tx.Exec(ctx, `UPDATE main_thread_handoffs SET target_thread_id=?,target_path=?,phase='created',updated_at=? WHERE handoff_id=?`, threadID, path, a.now().UnixMilli(), id)
		return err
	})
}

// CommitHandoff switches only the unchanged, inactive source mapping. Queued
// user messages remain queued and will be handled once by the new front thread.
func (a *App) CommitHandoff(ctx context.Context, id string) (v store.MainThreadBinding, err error) {
	err = a.handoffWrite(ctx, func(tx *store.Tx) error {
		h, err := store.GetThreadHandoff(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		current, err := store.GetMainThreadBinding(ctx, tx.Q(), h.Instance)
		if err != nil {
			return err
		}
		if h.Phase == "committed" {
			if current.ThreadID == h.TargetThreadID {
				v = current
				return nil
			}
			return protocol.Errorf(protocol.CodeStaleRevision, "committed handoff is no longer current")
		}
		if h.Phase != "injected" || h.TargetThreadID == "" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "handoff target has not confirmed injection")
		}
		if current.ThreadID != h.SourceThreadID || current.Generation != h.SourceGeneration {
			return protocol.Errorf(protocol.CodeStaleRevision, "main thread changed before handoff")
		}
		active, err := store.HasActiveMainTurn(ctx, tx.Q(), current.ThreadID)
		if err != nil {
			return err
		}
		if active {
			return protocol.Errorf(protocol.CodeInvalidTransition, "main thread still has an active turn")
		}
		ancestors := append([]store.ThreadAncestor{}, current.Ancestors...)
		ancestors = append(ancestors, store.ThreadAncestor{ThreadID: current.ThreadID, RolloutPath: current.RolloutPath})
		v = store.MainThreadBinding{Instance: h.Instance, ThreadID: h.TargetThreadID, RolloutPath: h.TargetRolloutPath, ToolsetHash: h.ToolsetHash, Generation: current.Generation + 1, Ancestors: ancestors}
		if err := tx.PutMainThreadBinding(ctx, v, a.now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.PutMainThread(ctx, v.Instance, v.ThreadID, a.now().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE main_thread_handoffs SET phase='committed',updated_at=? WHERE handoff_id=?`, a.now().UnixMilli(), id); err != nil {
			return err
		}
		return a.handoffEvent(ctx, tx, id, "conversation.handoff_completed")
	})
	return
}

// ClaimSummaryAttempt durably throttles the same source snapshot across restarts.
// retryAfterMillis is a duration, measured against the injected application clock.
func (a *App) ClaimSummaryAttempt(ctx context.Context, snap store.HandoffSnapshot, retryAfterMillis int64) (claimed bool, err error) {
	if retryAfterMillis < 0 || snap.Instance == "" || snap.ThreadID == "" || snap.Active || snap.Pending {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "summary attempt needs a quiescent snapshot and nonnegative backoff")
	}
	err = a.handoffWrite(ctx, func(tx *store.Tx) error {
		current, err := store.GetHandoffSnapshot(ctx, tx.Q(), snap.Instance)
		if err != nil {
			return err
		}
		if !snap.Covers(current) {
			return nil
		}
		now := a.now().UnixMilli()
		if retryAfterMillis > math.MaxInt64-now {
			return protocol.Errorf(protocol.CodeInvalidRequest, "summary backoff is too large")
		}
		var done bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_summaries WHERE instance=? AND source_thread_id=? AND source_version=?)`, snap.Instance, snap.ThreadID, snap.Version).Scan(&done); err != nil {
			return err
		}
		if done {
			return nil
		}
		result, err := tx.Exec(ctx, `INSERT INTO conversation_summary_attempts(instance,source_thread_id,source_version,binding_generation,status,retry_at,updated_at) VALUES(?,?,?,?,'started',?,?) ON CONFLICT(instance,source_thread_id,source_version,binding_generation) DO UPDATE SET status='started',retry_at=excluded.retry_at,updated_at=excluded.updated_at WHERE conversation_summary_attempts.status<>'succeeded' AND conversation_summary_attempts.retry_at<=?`, snap.Instance, snap.ThreadID, snap.Version, snap.BindingGeneration, now+retryAfterMillis, now, now)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		claimed = n == 1
		if err == nil && claimed {
			return a.handoffEvent(ctx, tx, snap.Instance, "conversation.summary_started")
		}
		return err
	})
	return
}

// FinishSummaryAttempt records completion without changing conversation coverage.
func (a *App) FinishSummaryAttempt(ctx context.Context, snap store.HandoffSnapshot, success bool) error {
	status := "failed"
	if success {
		status = "succeeded"
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE conversation_summary_attempts SET status=?,updated_at=? WHERE instance=? AND source_thread_id=? AND source_version=? AND binding_generation=? AND status<>?`, status, a.now().UnixMilli(), snap.Instance, snap.ThreadID, snap.Version, snap.BindingGeneration, status)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if !success && n > 0 {
			return a.handoffEvent(ctx, tx, snap.Instance, "conversation.summary_failed")
		}
		return nil
	})
}

// RecordMainThreadLocation records a trusted native history index without
// adopting a legacy toolset hash or advancing the binding generation.
func (a *App) RecordMainThreadLocation(ctx context.Context, instance, threadID, path string) error {
	if instance == "" || threadID == "" || path == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "native thread and rollout path are required")
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		current, err := store.GetMainThreadBinding(ctx, tx.Q(), instance)
		if err != nil {
			return err
		}
		if current.ThreadID != threadID {
			return protocol.Errorf(protocol.CodeStaleRevision, "main thread changed before location readback")
		}
		_, err = tx.Exec(ctx, `INSERT INTO main_thread_bindings(instance,thread_id,toolset_hash,path,generation,ancestors_json,created_at) VALUES(?,?,'',?,0,'[]',?) ON CONFLICT(instance,thread_id) DO UPDATE SET path=excluded.path`, instance, threadID, path, a.now().UnixMilli())
		return err
	})
}

// RefreshHandoff replaces a confirmed intent's reference package without
// creating another target. A previously injected package must be injected again.
func (a *App) RefreshHandoff(ctx context.Context, id string, sourceVersion int64, prompt string) error {
	if sourceVersion < 0 || strings.TrimSpace(prompt) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "handoff reference and source version are required")
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		job, err := store.GetThreadHandoff(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if job.Phase != "prepared" && job.Phase != "created" && job.Phase != "injected" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "handoff has an unconfirmed or completed external operation")
		}
		current, err := store.GetHandoffSnapshot(ctx, tx.Q(), job.Instance)
		if err != nil {
			return err
		}
		if current.ThreadID != job.SourceThreadID || current.BindingGeneration != job.SourceGeneration {
			return protocol.Errorf(protocol.CodeStaleRevision, "handoff source changed")
		}
		if sourceVersion < job.SourceVersion || sourceVersion > current.Version {
			return protocol.Errorf(protocol.CodeStaleRevision, "handoff source version is stale or unobserved")
		}
		if sourceVersion == job.SourceVersion && prompt == job.Prompt {
			return nil
		}
		phase := job.Phase
		if phase == "injected" {
			phase = "created"
		}
		_, err = tx.Exec(ctx, `UPDATE main_thread_handoffs SET source_version=?,prompt=?,phase=?,updated_at=? WHERE handoff_id=?`, sourceVersion, prompt, phase, a.now().UnixMilli(), id)
		return err
	})
}

// ConversationMaintenance reads the shared conversation's latest owner intent.
// Delivery failure reasons have the existing owner-safe DeliveryResult contract.
func (a *App) ConversationMaintenance(ctx context.Context, instance string) (v *protocol.ConversationMaintenanceView, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		var row protocol.ConversationMaintenanceView
		err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(NULLIF(o.parent_request_id,''),o.request_id),o.operation,o.status,COALESCE((SELECT json_extract(e.body_json,'$.reason') FROM events e WHERE e.object_type='outbox' AND e.object_id=o.outbox_id AND e.kind IN ('outbox.failed','outbox.unknown') ORDER BY e.event_seq DESC LIMIT 1),'') FROM outbox o WHERE o.destination='main_thread' AND o.operation='conversation.handoff' ORDER BY o.seq DESC LIMIT 1`).Scan(&row.RequestID, &row.Operation, &row.Status, &row.Reason)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		v = &row
		return nil
	})
	return
}

// RetargetPreparedHandoff updates an intent whose create was never attempted or
// was explicitly rejected. Confirmed targets and uncertain operations are kept.
func (a *App) RetargetPreparedHandoff(ctx context.Context, id, newHash string, newSourceVersion int64, prompt string) error {
	if newHash == "" || newSourceVersion < 0 || strings.TrimSpace(prompt) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "handoff needs toolset hash, observed version and reference")
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		job, err := store.GetThreadHandoff(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if job.Phase != "prepared" || job.TargetThreadID != "" || job.TargetRolloutPath != "" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "only a confirmed uncreated intent can be retargeted")
		}
		current, err := store.GetHandoffSnapshot(ctx, tx.Q(), job.Instance)
		if err != nil {
			return err
		}
		if current.ThreadID != job.SourceThreadID || current.BindingGeneration != job.SourceGeneration {
			return protocol.Errorf(protocol.CodeStaleRevision, "handoff source changed")
		}
		if newSourceVersion < job.SourceVersion || newSourceVersion != current.Version {
			return protocol.Errorf(protocol.CodeStaleRevision, "handoff reference no longer matches observed source")
		}
		if job.ToolsetHash == newHash && job.SourceVersion == newSourceVersion && job.Prompt == prompt {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE main_thread_handoffs SET toolset_hash=?,source_version=?,prompt=?,updated_at=? WHERE handoff_id=?`, newHash, newSourceVersion, prompt, a.now().UnixMilli(), id)
		return err
	})
}
