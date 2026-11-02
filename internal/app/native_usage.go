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

// UploadNativeUsage admits historical measurement after lease expiry without
// granting authority to execute, resume, or alter the underlying business run.
type UploadNativeUsage struct{}

// RegisterNativeUsage binds only the authenticated host's own run. Like the
// pre-existing run usage.source receipt, it grants no business execution rights.
// The host boundary lets retained journal evidence survive lease retirement.
type RegisterNativeUsage struct{}

func (RegisterNativeUsage) Operation() string { return protocol.OpRunnerUsageRegister }
func (RegisterNativeUsage) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRunner || x.Env.Target.Type != "host" || x.Env.Target.ID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "source registration requires its own host")
	}
	var p protocol.NativeUsageRegistration
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	run, err := store.GetRun(ctx, q, p.RunID)
	if err != nil {
		return err
	}
	if run.HostID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "usage run belongs to another host")
	}
	return nil
}
func (RegisterNativeUsage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.NativeUsageRegistration
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	run, err := store.GetRun(ctx, tx.Q(), p.RunID)
	if err != nil {
		return Outcome{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Outcome{}, err
	}
	if err := registerUsageSource(ctx, tx, run, raw, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]bool{"registered": true}}, nil
}

func (UploadNativeUsage) Operation() string { return protocol.OpRunnerUsageUpload }
func (UploadNativeUsage) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRunner || x.Env.Target.Type != "host" || x.Env.Target.ID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "usage upload requires its registered host")
	}
	var p protocol.UsageUpload
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	r, err := store.GetRun(ctx, q, p.RunID)
	if err != nil {
		return err
	}
	if r.HostID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "usage run belongs to another host")
	}
	if p.RootThreadID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "usage source identity is required")
	}
	var known bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_sources WHERE run_id=? AND host_id=? AND thread_id=?) OR EXISTS(SELECT 1 FROM main_turns t JOIN runner_coordination_executions e ON e.execution_id=t.request_id WHERE json_extract(e.private_snapshot,'$.runner_run_id')=? AND t.thread_id=?) OR EXISTS(SELECT 1 FROM run_logs WHERE run_id=? AND kind='worker.native_identity' AND json_extract(body_json,'$.state')='known' AND json_extract(body_json,'$.session_id')=?) OR EXISTS(SELECT 1 FROM token_usage_facts WHERE subject='run:'||? AND json_extract(payload_json,'$.thread_id')=?) OR EXISTS(SELECT 1 FROM run_logs WHERE run_id=? AND kind IN ('native.turn_started','native.turn_completed','item.completed') AND json_extract(body_json,'$.thread_id')=?)`, p.RunID, r.HostID, p.RootThreadID, p.RunID, p.RootThreadID, p.RunID, p.RootThreadID, p.RunID, p.RootThreadID, p.RunID, p.RootThreadID).Scan(&known)
	if err != nil {
		return err
	}
	if !known {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "native usage source registration is pending")
	}
	return nil
}

func (UploadNativeUsage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.UsageUpload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if len(p.Samples) > 256 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "usage batch exceeds record limit")
	}
	r, err := store.GetRun(ctx, tx.Q(), p.RunID)
	if err != nil {
		return Outcome{}, err
	}
	conversation := ""
	if r.Role == protocol.RoleIssueManager {
		if err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE(conversation_id,'') FROM issue_manager_tasks WHERE task_id=?`, r.TaskID).Scan(&conversation); err != nil {
			return Outcome{}, err
		}
	}
	if p.ConversationID != "" && p.ConversationID != conversation {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "usage conversation does not belong to this execution")
	}
	now := x.Now.UnixMilli()
	session := p.SessionID
	for _, sample := range p.Samples {
		if sample.ThreadID == p.RootThreadID {
			session = sample.SessionID
			break
		}
	}
	var prior string
	priorErr := tx.Q().QueryRowContext(ctx, `SELECT session_id FROM token_usage_sources WHERE run_id=? AND thread_id=?`, r.RunID, p.RootThreadID).Scan(&prior)
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return Outcome{}, priorErr
	}
	if session == "" {
		session = prior
	}
	if prior != "" && session != prior {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "native session binding changed")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO token_usage_sources(source_key,host_id,run_id,conversation_id,thread_id,session_id,provider,coverage,updated_at) VALUES(?,?,?,?,?,?,?,'pending',?) ON CONFLICT(source_key) DO UPDATE SET session_id=CASE WHEN token_usage_sources.session_id='' THEN excluded.session_id ELSE token_usage_sources.session_id END`, r.RunID+"/"+p.RootThreadID, r.HostID, r.RunID, conversation, p.RootThreadID, session, "native", now); err != nil {
		return Outcome{}, err
	}
	for _, sample := range p.Samples {
		if sample.RecordID == "" || sample.SessionID == "" || (sample.Provider != "codex" && sample.Provider != "traex") || !strings.HasPrefix(sample.Source, "native-ledger/"+sample.Provider+"/") {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid native measurement identity")
		}
		var provider string
		if err = tx.Q().QueryRowContext(ctx, `SELECT provider FROM token_usage_sources WHERE run_id=? AND thread_id=?`, p.RunID, p.RootThreadID).Scan(&provider); err != nil {
			return Outcome{}, err
		}
		if provider != "native" && provider != "" && provider != sample.Provider {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "native provider differs from its registered source")
		}
		if provider == "native" || provider == "" {
			if _, err = tx.Exec(ctx, `UPDATE token_usage_sources SET provider=? WHERE run_id=? AND thread_id=?`, sample.Provider, p.RunID, p.RootThreadID); err != nil {
				return Outcome{}, err
			}
		}
		if sample.ThreadID != p.RootThreadID {
			var session string
			err = tx.Q().QueryRowContext(ctx, `SELECT session_id FROM token_usage_sources WHERE run_id=? AND thread_id=?`, p.RunID, p.RootThreadID).Scan(&session)
			if err != nil || session == "" || sample.SessionID != session {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "child measurement requires an established session binding")
			}
		}
		var sourcePurpose, sourceParent string
		if err = tx.Q().QueryRowContext(ctx, `SELECT purpose,parent_session_id FROM token_usage_sources WHERE run_id=? AND thread_id=?`, p.RunID, p.RootThreadID).Scan(&sourcePurpose, &sourceParent); err != nil {
			return Outcome{}, err
		}
		if sample.Purpose == "" {
			sample.Purpose = sourcePurpose
		}
		if sample.ParentSessionID == "" {
			sample.ParentSessionID = sourceParent
		}
		var wrongSession bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_sources WHERE thread_id=? AND session_id<>'' AND session_id<>?)`, sample.ThreadID, sample.SessionID).Scan(&wrongSession); err != nil {
			return Outcome{}, err
		}
		if wrongSession {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "native thread belongs to another registered session")
		}
		attributedRun, proof, resolveErr := nativeUsageRun(ctx, tx.Q(), sample, p.RootThreadID, r.RunID, conversation)
		if resolveErr != nil {
			return Outcome{}, resolveErr
		}
		if attributedRun != "" {
			resolved, err := store.GetRun(ctx, tx.Q(), attributedRun)
			if err != nil {
				return Outcome{}, err
			}
			if resolved.HostID != x.Principal.Subject {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "resolved usage execution belongs to another host")
			}
		}
		enrichUsage(ctx, tx.Q(), &sample, attributedRun)

		sampleRole := string(r.Role)
		if sample.Purpose == "internal_summary" {
			sampleRole = "internal_summary"
		}
		subject := "run:" + attributedRun
		if conversation != "" {
			subject = "conversation:" + conversation
		}
		if err = recordTokenUsage(ctx, tx, sample, subject, sampleRole, now); err != nil {
			return Outcome{}, err
		}
		if err = bindNativeUsage(ctx, tx, sample, attributedRun, conversation, r.HostID, proof, now); err != nil {
			return Outcome{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE token_usage_sources SET coverage=?,updated_at=? WHERE run_id=? AND thread_id=?`, p.Coverage, now, p.RunID, p.RootThreadID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"accepted": len(p.Samples)}}, nil
}

func enrichUsage(ctx context.Context, q store.Querier, s *protocol.TokenUsageSample, run string) {
	if s.Model != "" {
		return
	}
	var model string
	if q.QueryRowContext(ctx, `SELECT model FROM main_turns WHERE thread_id=? AND turn_id=? LIMIT 1`, s.ThreadID, s.TurnID).Scan(&model) == nil && model != "" {
		s.Model = model
		s.ModelSource = "recorded-turn-execution"
		return
	}
	if run != "" && q.QueryRowContext(ctx, `SELECT json_extract(pin_json,'$.profile.model') FROM run_agent_executions WHERE run_id=?`, run).Scan(&model) == nil && model != "" {
		s.Model = model
		s.ModelSource = "frozen-execution"
	}
}

// reconcileUsageAliases keeps legacy evidence while counting its native request
// once. Synthetic response IDs are matched with thread, turn, and known values.
func reconcileUsageAliases(ctx context.Context, tx *store.Tx, s protocol.TokenUsageSample, now int64) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT f.seq,f.source,f.request_id,f.payload_json,c.conflict FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE json_extract(f.payload_json,'$.thread_id')=? AND json_extract(f.payload_json,'$.turn_id')=? AND json_extract(f.payload_json,'$.request_id')=?`, s.ThreadID, s.TurnID, s.RequestID)
	if err != nil {
		return err
	}
	type fact struct {
		seq        int64
		source, id string
		sample     protocol.TokenUsageSample
		conflict   bool
	}
	var natives, legacy []fact
	for rows.Next() {
		var f fact
		var raw string
		if err = rows.Scan(&f.seq, &f.source, &f.id, &raw, &f.conflict); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &f.sample); err != nil {
			break
		}
		if f.sample.RecordID != "" && !f.conflict {
			natives = append(natives, f)
		} else if f.sample.RecordID == "" && !f.conflict {
			legacy = append(legacy, f)
		}
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	for _, old := range legacy {
		var matches []fact
		for _, native := range natives {
			if knownCountersMatch(old.sample, native.sample) {
				matches = append(matches, native)
			}
		}
		for _, native := range natives {
			if _, err = tx.Exec(ctx, `INSERT INTO token_usage_legacy_candidates(legacy_seq,native_source,native_request_id,matched) VALUES(?,?,?,?) ON CONFLICT(legacy_seq,native_source,native_request_id) DO UPDATE SET matched=excluded.matched`, old.seq, native.source, native.id, knownCountersMatch(old.sample, native.sample)); err != nil {
				return err
			}
		}
		if len(matches) == 1 {
			n := matches[0]
			if _, err = tx.Exec(ctx, `INSERT INTO token_usage_legacy_links(legacy_seq,native_source,native_request_id,created_at) VALUES(?,?,?,?) ON CONFLICT(legacy_seq) DO NOTHING`, old.seq, n.source, n.id, now); err != nil {
				return err
			}
		} else if len(matches) > 1 {
			if _, err = tx.Exec(ctx, `UPDATE token_usage_current SET conflict=1 WHERE source=? AND request_id=?`, old.source, old.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func knownCountersMatch(a, b protocol.TokenUsageSample) bool {
	for _, pair := range [][2]*int64{{a.Input, b.Input}, {a.Output, b.Output}, {a.Total, b.Total}, {a.CacheRead, b.CacheRead}, {a.CacheWrite, b.CacheWrite}} {
		if pair[0] != nil && (pair[1] == nil || *pair[0] != *pair[1]) {
			return false
		}
	}
	return a.Total != nil || a.Input != nil || a.Output != nil
}

// RecordNativeConversationUsage is also used by hosted sessions and the owner
// reconciliation command; callers supply a pre-existing conversation binding.
func (a *App) RecordNativeConversationUsage(ctx context.Context, conversation string, samples []protocol.TokenUsageSample) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		id := store.ConversationID(conversation)
		if _, err := store.AgentConversation(ctx, tx.Q(), id); err != nil {
			return err
		}
		role := "main"
		if id != "main" {
			role = "consultation"
		}
		if strings.HasPrefix(id, "issue:") {
			role = "issue_manager"
		}
		for _, s := range samples {
			if s.RecordID == "" {
				return protocol.Errorf(protocol.CodeInvalidRequest, "native record identity is required")
			}
			enrichUsage(ctx, tx.Q(), &s, "")
			if s.Purpose == "internal_summary" {
				role = "internal_summary"
			}

			if err := recordTokenUsage(ctx, tx, s, "conversation:"+id, role, a.now().UnixMilli()); err != nil {
				return err
			}
			if err := bindNativeUsage(ctx, tx, s, "", id, "", "conversation-history", a.now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}

// RegisterUsageSource records a run-scoped native source, using the same
// authenticated event channel that already registers worker identities.
func registerUsageSource(ctx context.Context, tx *store.Tx, run store.RunRow, raw json.RawMessage, now int64) error {
	var p struct {
		ThreadID        string `json:"thread_id"`
		SessionID       string `json:"session_id"`
		Provider        string `json:"provider"`
		Purpose         string `json:"purpose"`
		ParentSessionID string `json:"parent_session_id"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ThreadID == "" || (p.Provider != "codex" && p.Provider != "traex") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid native usage source")
	}
	_, err := tx.Exec(ctx, `INSERT INTO token_usage_sources(source_key,host_id,run_id,thread_id,session_id,provider,purpose,parent_session_id,coverage,updated_at) VALUES(?,?,?,?,?,?,?,?,'pending',?) ON CONFLICT(source_key) DO NOTHING`, run.RunID+"/"+p.ThreadID, run.HostID, run.RunID, p.ThreadID, p.SessionID, p.Provider, p.Purpose, p.ParentSessionID, now)
	return err
}
