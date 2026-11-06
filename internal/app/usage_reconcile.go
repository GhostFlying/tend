package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (a *App) UsageTargets(ctx context.Context, p protocol.Principal) ([]protocol.UsageTarget, error) {
	out := []protocol.UsageTarget{}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var query string
		var args []any
		if p.Kind == protocol.PrincipalRunner {
			query = `SELECT DISTINCT r.run_id,COALESCE(e.conversation_id,''),t.thread_id,'','','' FROM runs r JOIN runner_coordination_executions e ON json_extract(e.private_snapshot,'$.runner_run_id')=r.run_id JOIN main_turns t ON t.request_id=e.execution_id WHERE r.host_id=? UNION SELECT DISTINCT l.run_id,'',json_extract(l.body_json,'$.session_id'),'','','' FROM run_logs l JOIN runs r ON r.run_id=l.run_id WHERE r.host_id=? AND l.kind='worker.native_identity' AND json_extract(l.body_json,'$.state')='known' UNION SELECT run_id,conversation_id,thread_id,provider,purpose,parent_session_id FROM token_usage_sources WHERE host_id=? UNION SELECT DISTINCT r.run_id,'',json_extract(f.payload_json,'$.thread_id'),'','','' FROM token_usage_facts f JOIN runs r ON f.subject='run:'||r.run_id WHERE r.host_id=? UNION SELECT DISTINCT l.run_id,'',json_extract(l.body_json,'$.thread_id'),'','','' FROM run_logs l JOIN runs r ON r.run_id=l.run_id WHERE r.host_id=? AND l.kind IN ('native.turn_started','native.turn_completed','item.completed')`
			args = []any{p.Subject, p.Subject, p.Subject, p.Subject, p.Subject}
		} else if p.Kind == protocol.PrincipalOwner {
			query = `SELECT '',conversation_id,thread_id,'','','' FROM agent_thread_history UNION SELECT '',instance,thread_id,'','','' FROM main_thread_bindings WHERE instance IN (SELECT conversation_id FROM agent_conversations) UNION SELECT DISTINCT '',conversation_id,thread_id,'','','' FROM agent_rollout_events UNION SELECT run_id,conversation_id,json_extract(source_json,'$.thread_id'),json_extract(source_json,'$.provider'),COALESCE(json_extract(source_json,'$.purpose'),''),COALESCE(json_extract(source_json,'$.parent_session_id'),'') FROM token_usage_local_sources`
		} else {
			return protocol.Errorf(protocol.CodeForbidden, "usage sources require owner or host")
		}
		rows, err := tx.Q().QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t protocol.UsageTarget
			var thread sql.NullString
			if err = rows.Scan(&t.RunID, &t.ConversationID, &thread, &t.Provider, &t.Purpose, &t.ParentSessionID); err != nil {
				return err
			}
			if !thread.Valid || thread.String == "" {
				continue
			}
			t.ThreadID = thread.String
			if p.Kind == protocol.PrincipalRunner && t.RunID != "" {
				var central bool
				if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_local_sources WHERE run_id=?)`, t.RunID).Scan(&central); err != nil {
					return err
				}
				if central {
					continue
				}
			}
			if t.RunID != "" {
				_ = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN leases l ON l.run_id=r.run_id WHERE r.run_id=? AND r.observed_state IN ('finished','failed') AND l.status='released')`, t.RunID).Scan(&t.Stopped)
			} else {
				_ = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_local_sources WHERE conversation_id=? AND json_extract(source_json,'$.thread_id')=? AND writer_stopped=1)`, t.ConversationID, t.ThreadID).Scan(&t.Stopped)
			}
			if t.ThreadID != "" {
				out = append(out, t)
			}
		}
		return rows.Err()
	})
	return out, err
}

func authorizeConversationUsage(ctx context.Context, q store.Querier, p protocol.UsageReconcile) error {
	var known bool
	if p.RunID != "" {
		if p.ConversationID != "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "choose a run or conversation source")
		}
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_local_sources WHERE run_id=? AND json_extract(source_json,'$.thread_id')=?)`, p.RunID, p.RootThreadID).Scan(&known); err != nil {
			return err
		}
	} else if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_thread_history WHERE conversation_id=? AND thread_id=?) OR EXISTS(SELECT 1 FROM main_thread_bindings WHERE instance=? AND thread_id=?) OR EXISTS(SELECT 1 FROM token_usage_local_sources WHERE conversation_id=? AND json_extract(source_json,'$.thread_id')=?) OR EXISTS(SELECT 1 FROM agent_rollout_events WHERE conversation_id=? AND thread_id=?)`, p.ConversationID, p.RootThreadID, p.ConversationID, p.RootThreadID, p.ConversationID, p.RootThreadID, p.ConversationID, p.RootThreadID).Scan(&known); err != nil {
		return err
	}
	if !known {
		return protocol.Errorf(protocol.CodeForbidden, "unregistered conversation history")
	}
	if len(p.Samples) > 256 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "usage batch exceeds record limit")
	}
	session := p.SessionID
	if session == "" {
		session = p.RootThreadID
	}
	for _, s := range p.Samples {
		if s.RecordID == "" || (s.ThreadID != p.RootThreadID && s.SessionID != session) {
			return protocol.Errorf(protocol.CodeForbidden, "measurement is outside the registered session")
		}
	}
	return nil
}

type ReconcileConversationUsage struct{}

func (ReconcileConversationUsage) Operation() string { return "token_usage.reconcile" }
func (ReconcileConversationUsage) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalOwner {
		return protocol.Errorf(protocol.CodeForbidden, "owner reconciliation required")
	}
	var p protocol.UsageReconcile
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	return authorizeConversationUsage(ctx, q, p)
}
func (ReconcileConversationUsage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.UsageReconcile
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	session := p.SessionID
	for _, sample := range p.Samples {
		if sample.ThreadID == p.RootThreadID {
			session = sample.SessionID
			break
		}
	}
	if session == "" {
		session = p.RootThreadID
	}
	scope := "conversation:" + p.ConversationID
	if p.RunID != "" {
		scope = "run:" + p.RunID
	}
	key := scope + "/" + p.RootThreadID
	var previous string
	e := tx.Q().QueryRowContext(ctx, `SELECT session_id FROM token_usage_sources WHERE source_key=?`, key).Scan(&previous)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return Outcome{}, e
	}
	if previous != "" && previous != session {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "conversation session binding changed")
	}
	coverage := p.Coverage
	if coverage == "" {
		coverage = "syncing"
	}
	if _, e = tx.Exec(ctx, `INSERT INTO token_usage_sources(source_key,run_id,conversation_id,thread_id,session_id,provider,coverage,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_key) DO UPDATE SET coverage=excluded.coverage,updated_at=excluded.updated_at`, key, p.RunID, p.ConversationID, p.RootThreadID, session, "native", coverage, x.Now.UnixMilli()); e != nil {
		return Outcome{}, e
	}
	role := "main"
	if p.ConversationID != "main" {
		role = "consultation"
	}
	if strings.HasPrefix(p.ConversationID, "issue:") {
		role = "issue_manager"
	}
	proof := "conversation-history"
	if p.RunID != "" {
		run, err := store.GetRun(ctx, tx.Q(), p.RunID)
		if err != nil {
			return Outcome{}, err
		}
		role, proof = string(run.Role), "registered-thread"
	}
	for _, s := range p.Samples {
		enrichUsage(ctx, tx.Q(), &s, p.RunID)
		sampleRole := role
		if s.Purpose == "internal_summary" {
			sampleRole = "internal_summary"
		}
		if err := recordTokenUsage(ctx, tx, s, scope, sampleRole, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		if err := bindNativeUsage(ctx, tx, s, p.RunID, p.ConversationID, "", proof, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: map[string]int{"accepted": len(p.Samples)}}, nil
}

// PreviewUsage exercises the same authorization, normalization, conflicts and
// alias reconciliation as an upload. The transaction always rolls back; no
// facts, cursors, request receipts, events or business effects are committed.
func (a *App) PreviewUsage(ctx context.Context, p protocol.Principal, input protocol.UsageUpload) (protocol.UsagePreview, error) {
	result := protocol.UsagePreview{}
	rollback := errors.New("usage preview rollback")
	err := a.write(ctx, func(tx *store.Tx) error {
		var command Command = UploadNativeUsage{}
		payload := any(input)
		target := protocol.Target{Type: "host", ID: p.Subject}
		if p.Kind == protocol.PrincipalOwner {
			command = ReconcileConversationUsage{}
			payload = protocol.UsageReconcile{RunID: input.RunID, ConversationID: input.ConversationID, RootThreadID: input.RootThreadID, SessionID: input.SessionID, Coverage: input.Coverage, Samples: input.Samples}
			target = protocol.Target{Type: "conversation", ID: input.ConversationID}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		x := &Exec{Principal: p, Env: &protocol.Envelope{Operation: command.Operation(), Target: target, Payload: raw}, Now: a.now()}
		if len(input.Samples) > 16384 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "usage preview exceeds record limit")
		}

		before, err := usagePreviewMetrics(ctx, tx.Q())
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, sample := range input.Samples {
			key := sample.Source + "/" + sample.RecordID
			if seen[key] {
				continue
			}
			seen[key] = true
			result.Records++
			var exists bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_current WHERE source=? AND request_id=?)`, sample.Source, sample.RecordID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				result.Existing++
			} else {
				result.New++
			}
		}
		for offset := 0; offset < len(input.Samples) || offset == 0; offset += 256 {
			end := min(len(input.Samples), offset+256)
			batch := input
			batch.Samples = input.Samples[offset:end]
			payload = any(batch)
			if p.Kind == protocol.PrincipalOwner {
				payload = protocol.UsageReconcile{RunID: input.RunID, ConversationID: input.ConversationID, RootThreadID: input.RootThreadID, SessionID: input.SessionID, Coverage: input.Coverage, Samples: batch.Samples}
			}
			x.Env.Payload, err = json.Marshal(payload)
			if err != nil {
				return err
			}
			if err = command.Authorize(ctx, x, tx.Q()); err != nil {
				return err
			}
			if _, err = command.Apply(ctx, x, tx); err != nil {
				return err
			}
			if end == len(input.Samples) {
				break
			}
		}
		after, err := usagePreviewMetrics(ctx, tx.Q())
		if err != nil {
			return err
		}
		result.TokenDelta = after.tokens - before.tokens
		result.LinkedLegacy = max(int64(0), after.links-before.links)
		for _, sample := range input.Samples {
			key := sample.Source + "/" + sample.RecordID
			if !seen[key] {
				continue
			}
			delete(seen, key)
			var conflict bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT c.conflict OR `+store.UsagePendingOverlapSQLFor("f")+` FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE c.source=? AND c.request_id=?`, sample.Source, sample.RecordID).Scan(&conflict); err != nil {
				return err
			}
			if conflict {
				result.Conflicts++
			}
		}
		return rollback
	})
	if errors.Is(err, rollback) {
		err = nil
	}
	return result, err
}

type previewMetrics struct{ tokens, links int64 }

func usagePreviewMetrics(ctx context.Context, q store.Querier) (previewMetrics, error) {
	var result previewMetrics
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(json_extract(f.payload_json,'$.total')),0) FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE c.conflict=0 AND NOT `+store.UsagePendingOverlapSQLFor("f")+` AND NOT `+store.UsageLegacyLinkedSQL("f.seq")).Scan(&result.tokens)
	if err != nil {
		return result, err
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM token_usage_facts f WHERE `+store.UsageLegacyLinkedSQL("f.seq")).Scan(&result.links)
	return result, err
}
