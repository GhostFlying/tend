package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type LocalUsageSource struct {
	RunID          string
	WriterStopped  bool
	Coverage       string
	ID             string
	ConversationID string
	Source         json.RawMessage
	Cursors        json.RawMessage
}

// RegisteredNativeUsageRuns supplies only ledger-backed identities to central
// history recovery. The broker must additionally match its private run binding.
func (a *App) RegisteredNativeUsageRuns(ctx context.Context) ([]string, error) {
	var runs []string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		runs, err = store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT run_id FROM run_logs WHERE kind='worker.native_identity' AND json_extract(body_json,'$.state')='known'`)
		return err
	})
	return runs, err
}

func (a *App) RegisterLocalUsage(ctx context.Context, s LocalUsageSource) error {
	if s.RunID == "" {
		s.ConversationID = store.ConversationID(s.ConversationID)
	}
	return a.write(ctx, func(tx *store.Tx) error {
		if s.RunID != "" {
			if _, err := store.GetRun(ctx, tx.Q(), s.RunID); err != nil {
				return err
			}
		} else if _, err := store.AgentConversation(ctx, tx.Q(), s.ConversationID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO token_usage_local_sources(source_key,run_id,conversation_id,source_json,writer_stopped,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(source_key) DO UPDATE SET writer_stopped=excluded.writer_stopped,coverage=CASE WHEN token_usage_local_sources.writer_stopped<>excluded.writer_stopped THEN 'pending' ELSE token_usage_local_sources.coverage END`, s.ID, s.RunID, s.ConversationID, string(s.Source), s.WriterStopped, a.now().UnixMilli())
		return err
	})
}

func (a *App) LocalUsageSources(ctx context.Context, conversation string) ([]LocalUsageSource, error) {
	var out []LocalUsageSource
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT source_key,run_id,conversation_id,source_json,cursors_json,coverage,writer_stopped FROM token_usage_local_sources WHERE (?='' OR conversation_id=?) ORDER BY source_key`, conversation, conversation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s LocalUsageSource
			var source, cursors string
			if err = rows.Scan(&s.ID, &s.RunID, &s.ConversationID, &source, &cursors, &s.Coverage, &s.WriterStopped); err != nil {
				return err
			}
			s.Source = json.RawMessage(source)
			s.Cursors = json.RawMessage(cursors)
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

func (a *App) CommitLocalUsage(ctx context.Context, s LocalUsageSource, samples []protocol.TokenUsageSample) error {
	if s.RunID == "" {
		s.ConversationID = store.ConversationID(s.ConversationID)
	}
	return a.write(ctx, func(tx *store.Tx) error {
		var registered, registeredRun string
		if err := tx.Q().QueryRowContext(ctx, `SELECT conversation_id,run_id FROM token_usage_local_sources WHERE source_key=?`, s.ID).Scan(&registered, &registeredRun); err != nil {
			return err
		}
		if registered != s.ConversationID || registeredRun != s.RunID {
			return protocol.Errorf(protocol.CodeForbidden, "usage source ownership changed")
		}
		role := "main"
		if registered != "main" {
			role = "consultation"
		}
		if strings.HasPrefix(registered, "issue:") {
			role = "issue_manager"
		}
		subject := "conversation:" + registered
		proof := "conversation-history"
		if registeredRun != "" {
			run, err := store.GetRun(ctx, tx.Q(), registeredRun)
			if err != nil {
				return err
			}
			role, subject, proof = string(run.Role), "run:"+registeredRun, "registered-thread"
		}
		for _, sample := range samples {
			enrichUsage(ctx, tx.Q(), &sample, registeredRun)
			sampleRole := role
			if sample.Purpose == "internal_summary" {
				sampleRole = "internal_summary"
			}

			if err := recordTokenUsage(ctx, tx, sample, subject, sampleRole, a.now().UnixMilli()); err != nil {
				return err
			}
			if err := bindNativeUsage(ctx, tx, sample, registeredRun, registered, "", proof, a.now().UnixMilli()); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE token_usage_local_sources SET source_json=?,cursors_json=?,coverage=?,updated_at=? WHERE source_key=?`, string(s.Source), string(s.Cursors), s.Coverage, a.now().UnixMilli(), s.ID)
		return err
	})
}

// StopLocalUsage records a native writer boundary without deleting its history.
// Empty thread closes every local source of the stopped owning harness.
func (a *App) StopLocalUsage(ctx context.Context, conversation, thread string) error {
	conversation = store.ConversationID(conversation)
	return a.write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE token_usage_local_sources SET writer_stopped=1 WHERE conversation_id=? AND (?='' OR json_extract(source_json,'$.thread_id')=?)`, conversation, thread, thread)
		return err
	})
}

func (a *App) StopRunUsage(ctx context.Context, run string) error {
	return a.write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE token_usage_local_sources SET writer_stopped=1 WHERE run_id=?`, run)
		return err
	})
}
