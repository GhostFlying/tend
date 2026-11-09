package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type LocalUsageSource struct {
	Collection       protocol.UsageCollection
	ExpectedRevision int64
	RunID            string
	WriterStopped    bool
	Coverage         string
	ID               string
	ConversationID   string
	Source           json.RawMessage
	Cursors          json.RawMessage
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
	err := a.write(ctx, func(tx *store.Tx) error {
		if s.RunID != "" {
			if _, err := store.GetRun(ctx, tx.Q(), s.RunID); err != nil {
				return err
			}
		} else if _, err := store.AgentConversation(ctx, tx.Q(), s.ConversationID); err != nil {
			return err
		}
		var metadata struct {
			NoDispatchProven bool   `json:"no_dispatch_proven"`
			WriterID         string `json:"writer_id"`
			Path             string `json:"path"`
			Provider         string `json:"provider"`
			ThreadID         string `json:"thread_id"`
			SessionID        string `json:"session_id"`
		}
		if err := json.Unmarshal(s.Source, &metadata); err != nil {
			return err
		}
		previous, err := localUsageSource(ctx, tx.Q(), s.ID)
		if err == sql.ErrNoRows && metadata.ThreadID != "" && metadata.Provider != "" {
			var known string
			e := tx.Q().QueryRowContext(ctx, `SELECT source_key FROM token_usage_local_sources WHERE run_id=? AND (?<>'' OR conversation_id=?) AND json_extract(source_json,'$.provider')=? AND json_extract(source_json,'$.thread_id')=? ORDER BY writer_stopped,source_key LIMIT 1`, s.RunID, s.RunID, s.ConversationID, metadata.Provider, metadata.ThreadID).Scan(&known)
			if e == nil {
				s.ID = known
				previous, err = localUsageSource(ctx, tx.Q(), known)
			} else if e != sql.ErrNoRows {
				return e
			}
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if previous.RunID != s.RunID || previous.ConversationID != s.ConversationID {
				return protocol.Errorf(protocol.CodeForbidden, "usage source ownership changed")
			}
			var prior struct{ Path, SessionID string }
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(previous.Source, &fields); err != nil {
				return err
			}
			_ = json.Unmarshal(fields["path"], &prior.Path)
			_ = json.Unmarshal(fields["session_id"], &prior.SessionID)
			if prior.SessionID != "" && metadata.SessionID != "" && prior.SessionID != metadata.SessionID {
				return protocol.Errorf(protocol.CodeForbidden, "usage source session changed")
			}
			if previous.WriterStopped == s.WriterStopped && (metadata.WriterID == "" || metadata.WriterID == previous.Collection.WriterID) {
				if prior.Path == metadata.Path {
					return nil
				}
				var current map[string]json.RawMessage
				if err := json.Unmarshal(s.Source, &current); err != nil {
					return err
				}
				if metadata.SessionID == "" && prior.SessionID != "" {
					current["session_id"], _ = json.Marshal(prior.SessionID)
				}
				if metadata.WriterID == "" && previous.Collection.WriterID != "" {
					current["writer_id"], _ = json.Marshal(previous.Collection.WriterID)
				}
				previous.Source, _ = json.Marshal(current)
				var cursors map[string]json.RawMessage
				if err := json.Unmarshal(previous.Cursors, &cursors); err != nil {
					return err
				}
				if cursor, ok := cursors[prior.Path]; ok {
					if _, exists := cursors[metadata.Path]; !exists {
						cursors[metadata.Path] = cursor
						previous.Cursors, _ = json.Marshal(cursors)
					}
				}
				return saveLocalUsage(ctx, tx, previous, a.now().UnixMilli())
			}
			if s.WriterStopped && previous.Collection.WriterID != "" && metadata.WriterID != previous.Collection.WriterID {
				return protocol.Errorf(protocol.CodeStaleRevision, "stopped usage writer changed")
			}
			if !s.WriterStopped && !previous.WriterStopped && previous.Collection.WriterID != "" && metadata.WriterID != previous.Collection.WriterID {
				return protocol.Errorf(protocol.CodeStaleRevision, "original usage writer has not stopped")
			}
			if !s.WriterStopped {
				previous.Collection.Activate("", metadata.WriterID)
				previous.Collection.WriterID = metadata.WriterID
				previous.Collection.StopProof = ""
			}
			previous.WriterStopped = s.WriterStopped
			if metadata.SessionID == "" && prior.SessionID != "" {
				var current map[string]json.RawMessage
				if err := json.Unmarshal(s.Source, &current); err != nil {
					return err
				}
				current["session_id"], _ = json.Marshal(prior.SessionID)
				s.Source, _ = json.Marshal(current)
			}
			previous.Source = s.Source
			previous.Coverage = "pending"
			if s.WriterStopped {
				empty, err := localUsageEmpty(ctx, tx.Q(), previous)
				if err != nil {
					return err
				}
				previous.Collection.Stopped(a.now().UnixMilli(), "registered-stopped-writer", empty)
			}
			return saveLocalUsage(ctx, tx, previous, a.now().UnixMilli())
		}
		s.Collection = protocol.NewUsageCollection(metadata.NoDispatchProven, s.WriterStopped)
		s.Collection.WriterID = metadata.WriterID
		if s.WriterStopped {
			empty, err := localUsageEmpty(ctx, tx.Q(), s)
			if err != nil {
				return err
			}
			s.Collection.Stopped(a.now().UnixMilli(), "registered-stopped-writer", empty)
		}
		raw, err := json.Marshal(s.Collection)
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `INSERT INTO token_usage_local_sources(source_key,run_id,conversation_id,source_json,writer_stopped,collection_json,collection_state,updated_at) VALUES(?,?,?,?,?,?,?,?)`, s.ID, s.RunID, s.ConversationID, string(s.Source), s.WriterStopped, string(raw), s.Collection.State, a.now().UnixMilli())
		return usageCollectionChanged(tx, result, err)
	})
	if err == nil {
		a.wakeLocalUsage()
	}
	return err
}

func (a *App) LocalUsageSources(ctx context.Context, conversation string) ([]LocalUsageSource, error) {
	return a.localUsageSources(ctx, conversation, false)
}
func (a *App) localUsageSources(ctx context.Context, conversation string, pending bool) ([]LocalUsageSource, error) {
	var out []LocalUsageSource
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		query := `SELECT source_key,run_id,conversation_id,source_json,cursors_json,coverage,writer_stopped,collection_json FROM token_usage_local_sources WHERE (?='' OR conversation_id=?)`
		if pending {
			query += ` AND collection_state IN ('active','settling')`
		}
		rows, err := tx.Q().QueryContext(ctx, query+` ORDER BY source_key`, conversation, conversation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s LocalUsageSource
			var source, cursors, collection string
			if err = rows.Scan(&s.ID, &s.RunID, &s.ConversationID, &source, &cursors, &s.Coverage, &s.WriterStopped, &collection); err != nil {
				return err
			}
			if err = json.Unmarshal([]byte(collection), &s.Collection); err != nil {
				return err
			}
			s.ExpectedRevision = s.Collection.Revision
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
		current, err := localUsageSource(ctx, tx.Q(), s.ID)
		if err != nil {
			return err
		}
		if current.WriterStopped != s.WriterStopped || current.Collection.Epoch != s.Collection.Epoch || current.Collection.Revision != s.ExpectedRevision {
			return protocol.Errorf(protocol.CodeStaleRevision, "usage source lifecycle changed")
		}
		registered, registeredRun := current.ConversationID, current.RunID
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
		return saveLocalUsage(ctx, tx, s, a.now().UnixMilli())
	})
}

// StopLocalUsage records a native writer boundary without deleting its history.
// Empty thread closes every local source of the stopped owning harness.
func (a *App) StopLocalUsage(ctx context.Context, conversation, thread string, writerIDs ...string) error {
	writer := ""
	if len(writerIDs) > 0 {
		writer = writerIDs[0]
	}
	conversation = store.ConversationID(conversation)
	err := a.write(ctx, func(tx *store.Tx) error {
		return stopLocalUsage(ctx, tx, conversation, thread, "", writer, a.now().UnixMilli())
	})
	if err == nil {
		a.wakeLocalUsage()
	}
	return err
}

func (a *App) StopRunUsage(ctx context.Context, run string, writerIDs ...string) error {
	writer := ""
	if len(writerIDs) > 0 {
		writer = writerIDs[0]
	}
	err := a.write(ctx, func(tx *store.Tx) error {
		return stopLocalUsage(ctx, tx, "", "", run, writer, a.now().UnixMilli())
	})
	if err == nil {
		a.wakeLocalUsage()
	}
	return err
}

// LocalUsageWake is consumed by the central collector, after durable lifecycle changes.
func (a *App) LocalUsageWake() <-chan struct{} { return a.usageWake }
func (a *App) wakeLocalUsage() {
	select {
	case a.usageWake <- struct{}{}:
	default:
	}
}

// PendingLocalUsageSources excludes settled history before decoding its cursors.
func (a *App) PendingLocalUsageSources(ctx context.Context, conversation string) ([]LocalUsageSource, error) {
	return a.localUsageSources(ctx, conversation, true)
}

func usageCollectionChanged(tx *store.Tx, result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count > 0 {
		tx.MarkChanged("token_usage")
	}
	return err
}
