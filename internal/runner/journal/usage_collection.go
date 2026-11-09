package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type usageSourceReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type UsageSourceSnapshot struct {
	Source           UsageSource `json:"source"`
	ExpectedRevision int64       `json:"expected_revision"`
}

type UsageQueueRequest struct {
	UsageSourceSnapshot
	Envelope *protocol.Envelope `json:"envelope"`
}

func (j *Journal) UsageSourceSnapshot(ctx context.Context, run, provider, thread string) (UsageSourceSnapshot, error) {
	id, err := j.UsageSourceID(ctx, run, provider, thread)
	if err != nil {
		return UsageSourceSnapshot{}, err
	}
	s, err := getUsageSource(ctx, j.db, id)
	return UsageSourceSnapshot{Source: s, ExpectedRevision: s.ExpectedRevision}, err
}

func getUsageSource(ctx context.Context, tx usageSourceReader, id string) (UsageSource, error) {
	var s UsageSource
	var raw string
	var stopped bool
	err := tx.QueryRowContext(ctx, `SELECT state_json,writer_stopped FROM native_usage_sources WHERE source_id=?`, id).Scan(&raw, &stopped)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal([]byte(raw), &s); err != nil {
		return s, err
	}
	s.WriterStopped = stopped
	s.ExpectedRevision = s.Collection.Revision
	return s, nil
}

func saveUsageSource(ctx context.Context, tx *sql.Tx, s UsageSource, env *protocol.Envelope, now int64) error {
	old, err := getUsageSource(ctx, tx, s.ID)
	if err != nil {
		return err
	}
	if s.ExpectedRevision != old.Collection.Revision || s.Collection.Epoch != old.Collection.Epoch {
		return protocol.Errorf(protocol.CodeStaleRevision, "native usage lifecycle changed")
	}
	s.RegistrationQueued = old.RegistrationQueued
	before, _ := json.Marshal(old)
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if string(before) != string(body) {
		if s.Collection.Revision <= old.Collection.Revision {
			s.Collection.Revision = old.Collection.Revision + 1
		}
		body, err = json.Marshal(s)
		if err != nil {
			return err
		}
		if s.Collection.State != old.Collection.State || s.Collection.Epoch != old.Collection.Epoch || old.Source.Path != s.Source.Path {
			if _, err = tx.ExecContext(ctx, `INSERT INTO native_usage_collection_history(source_id,epoch,revision,state_json,recorded_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, s.ID, old.Collection.Epoch, old.Collection.Revision, string(before), now); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET state_json=?,writer_stopped=?,collection_state=?,collection_report_pending=1 WHERE source_id=?`, string(body), s.WriterStopped, s.Collection.State, s.ID); err != nil {
			return err
		}
	}
	if env != nil {
		return putRequest(ctx, tx, Request{Envelope: *env}, time.UnixMilli(now))
	}
	return nil
}

// ActivateUsageSource records the intent before native turn dispatch. The
// journal transaction fences an old collector without changing its saved cursor.
func (j *Journal) ActivateUsageSource(ctx context.Context, id, request string) error {
	err := j.tx(ctx, func(tx *sql.Tx) error {
		s, err := getUsageSource(ctx, tx, id)
		if err != nil {
			return err
		}
		if s.WriterStopped {
			return protocol.Errorf(protocol.CodeStaleRevision, "native writer is stopped")
		}
		// A retained manager can dispatch several native turns under one run
		// reference. Every dispatch fences earlier collection work, even when
		// the enclosing business request has not changed.
		s.Collection.Activate(request, s.Source.WriterID)
		// An activation deliberately advances the epoch under the same transaction.
		return replaceUsageCollection(ctx, tx, s, j.now().UnixMilli())
	})
	if err == nil {
		j.WakeUsage()
	}
	return err
}

func replaceUsageCollection(ctx context.Context, tx *sql.Tx, s UsageSource, now int64) error {
	old, err := getUsageSource(ctx, tx, s.ID)
	if err != nil {
		return err
	}
	if old.Collection.Revision != s.ExpectedRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "native usage changed")
	}
	before, _ := json.Marshal(old)
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_usage_collection_history(source_id,epoch,revision,state_json,recorded_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, s.ID, old.Collection.Epoch, old.Collection.Revision, string(before), now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET state_json=?,writer_stopped=?,collection_state=?,collection_report_pending=1 WHERE source_id=?`, string(body), s.WriterStopped, s.Collection.State, s.ID)
	return err
}

func (j *Journal) UsageSourceID(ctx context.Context, run, provider, thread string) (string, error) {
	var id string
	err := j.db.QueryRowContext(ctx, `SELECT source_id FROM native_usage_sources WHERE run_id=? AND json_extract(state_json,'$.source.provider')=? AND json_extract(state_json,'$.source.thread_id')=? ORDER BY source_id LIMIT 1`, run, provider, thread).Scan(&id)
	return id, err
}
