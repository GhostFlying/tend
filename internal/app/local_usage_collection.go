package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func localUsageSource(ctx context.Context, q store.Querier, id string) (LocalUsageSource, error) {
	var s LocalUsageSource
	var source, cursors, collection string
	err := q.QueryRowContext(ctx, `SELECT source_key,run_id,conversation_id,source_json,cursors_json,coverage,writer_stopped,collection_json FROM token_usage_local_sources WHERE source_key=?`, id).Scan(&s.ID, &s.RunID, &s.ConversationID, &source, &cursors, &s.Coverage, &s.WriterStopped, &collection)
	if err != nil {
		return s, err
	}
	s.Source, s.Cursors = json.RawMessage(source), json.RawMessage(cursors)
	err = json.Unmarshal([]byte(collection), &s.Collection)
	s.ExpectedRevision = s.Collection.Revision
	return s, err
}

func saveLocalUsage(ctx context.Context, tx *store.Tx, s LocalUsageSource, now int64) error {
	previous, err := localUsageSource(ctx, tx.Q(), s.ID)
	if err != nil {
		return err
	}
	if previous.Collection.Revision != s.ExpectedRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "usage collection changed")
	}
	if s.Collection == previous.Collection && string(s.Source) == string(previous.Source) && string(s.Cursors) == string(previous.Cursors) && s.Coverage == previous.Coverage && s.WriterStopped == previous.WriterStopped {
		return nil
	}
	if s.Collection.Revision <= previous.Collection.Revision {
		s.Collection.Revision = previous.Collection.Revision + 1
	}
	raw, err := json.Marshal(s.Collection)
	if err != nil {
		return err
	}
	if s.Collection.State != previous.Collection.State || s.Collection.Epoch != previous.Collection.Epoch || string(s.Source) != string(previous.Source) {
		old, err := json.Marshal(previous.Collection)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO token_usage_collection_history(source_key,epoch,revision,collection_json,source_json,cursors_json,recorded_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, s.ID, previous.Collection.Epoch, previous.Collection.Revision, string(old), string(previous.Source), string(previous.Cursors), now); err != nil {
			return err
		}
	}
	result, err := tx.Exec(ctx, `UPDATE token_usage_local_sources SET source_json=?,cursors_json=?,coverage=?,writer_stopped=?,collection_json=?,collection_state=?,updated_at=? WHERE source_key=? AND json_extract(collection_json,'$.revision')=?`, string(s.Source), string(s.Cursors), s.Coverage, s.WriterStopped, string(raw), s.Collection.State, now, s.ID, s.ExpectedRevision)
	return usageCollectionChanged(tx, result, err)
}

func stopLocalUsage(ctx context.Context, tx *store.Tx, conversation, thread, run, writer string, now int64) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT source_key FROM token_usage_local_sources WHERE writer_stopped=0 AND ((?<>'' AND run_id=?) OR (?='' AND conversation_id=? AND (?='' OR json_extract(source_json,'$.thread_id')=?))) AND (?='' OR json_extract(collection_json,'$.writer_id')=?)`, run, run, run, conversation, thread, thread, writer, writer)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		s, err := localUsageSource(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		s.WriterStopped = true
		empty := string(s.Cursors) == "{}" && s.Collection.Evidence == protocol.UsageNoDispatch
		if empty {
			var thread string
			if err = tx.Q().QueryRowContext(ctx, `SELECT json_extract(source_json,'$.thread_id') FROM token_usage_local_sources WHERE source_key=?`, s.ID).Scan(&thread); err != nil {
				return err
			}
			var facts bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_facts WHERE json_extract(payload_json,'$.thread_id')=?)`, thread).Scan(&facts); err != nil {
				return err
			}
			empty = !facts
		}
		s.Collection.Stopped(now, "owned-writer-closed:"+s.Collection.WriterID, empty)
		if s.Collection.RetirementReason != "empty" {
			s.Coverage = "pending"
		}
		if err = saveLocalUsage(ctx, tx, s, now); err != nil {
			return err
		}
	}
	return nil
}

// ActivateLocalUsage commits dispatch evidence before the caller sends any
// native turn. A missing source is allowed only for harnesses with no usage path.
func (a *App) ActivateLocalUsage(ctx context.Context, conversation, thread, request string) error {
	return a.activateUsage(ctx, store.ConversationID(conversation), "", thread, request)
}
func (a *App) ActivateRunUsage(ctx context.Context, run, thread, request string) error {
	return a.activateUsage(ctx, "", run, thread, request)
}
func (a *App) activateUsage(ctx context.Context, conversation, run, thread, request string) error {
	err := a.write(ctx, func(tx *store.Tx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT source_key FROM token_usage_local_sources WHERE ((?<>'' AND run_id=?) OR (?='' AND conversation_id=?)) AND json_extract(source_json,'$.thread_id')=? AND writer_stopped=0`, run, run, run, store.ConversationID(conversation), thread)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			s, err := localUsageSource(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if s.WriterStopped {
				return protocol.Errorf(protocol.CodeStaleRevision, "native writer must be registered before execution")
			}
			if s.Collection.RequestID == request && s.Collection.State == protocol.UsageActive {
				continue
			}
			s.Collection.Activate(request, s.Collection.WriterID)
			if err = saveLocalUsage(ctx, tx, s, a.now().UnixMilli()); err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			return protocol.Errorf(protocol.CodeNotFound, "native usage source has not been registered")
		}
		return nil
	})
	if err == nil {
		a.wakeLocalUsage()
	}
	return err
}

// SettleLocalUsage wakes only the current reusable source for a real completion
// notification. Retired sources and never-dispatched metadata remain excluded.
func (a *App) SettleLocalUsage(ctx context.Context, conversation, thread string) error {
	return a.write(ctx, func(tx *store.Tx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT source_key FROM token_usage_local_sources WHERE conversation_id=? AND writer_stopped=0 AND collection_state='dormant' AND json_extract(collection_json,'$.execution_evidence')<>'no_dispatch_proven' AND (?='' OR json_extract(source_json,'$.thread_id')=?)`, store.ConversationID(conversation), thread, thread)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			s, err := localUsageSource(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			s.Collection.State = protocol.UsageSettling
			if err = saveLocalUsage(ctx, tx, s, a.now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}

// FailLocalUsage persists the finite reading budget without advancing cursors.
func (a *App) FailLocalUsage(ctx context.Context, s *LocalUsageSource, code string) error {
	next := *s
	next.Collection.Failed(a.now().UnixMilli(), next.WriterStopped, code)
	if code == "identity_mismatch" {
		next.Collection.RetireUnavailable(a.now().UnixMilli(), next.WriterStopped)
	}
	if err := a.CommitLocalUsage(ctx, next, nil); err != nil {
		return err
	}
	*s = next
	return nil
}
