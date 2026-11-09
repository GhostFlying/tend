package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
)

type UsageSource struct {
	Collection         protocol.UsageCollection      `json:"collection"`
	ExpectedRevision   int64                         `json:"-"`
	RegistrationQueued bool                          `json:"registration_queued,omitempty"`
	WriterStopped      bool                          `json:"writer_stopped,omitempty"`
	Coverage           string                        `json:"coverage"`
	ID                 string                        `json:"id"`
	RunID              string                        `json:"run_id"`
	ConversationID     string                        `json:"conversation_id"`
	Source             nativeusage.Source            `json:"source"`
	Cursors            map[string]nativeusage.Cursor `json:"cursors"`
	Files              []nativeusage.Source          `json:"files"`
	DiscoveredAt       int64                         `json:"discovered_at"`
}

func (j *Journal) RegisterUsageSource(ctx context.Context, s UsageSource) error {
	defer j.WakeUsage()
	return j.tx(ctx, func(tx *sql.Tx) error {
		previous, err := getUsageSource(ctx, tx, s.ID)
		if errors.Is(err, sql.ErrNoRows) && s.Source.ThreadID != "" && s.Source.Provider != "" {
			var id string
			e := tx.QueryRowContext(ctx, `SELECT source_id FROM native_usage_sources WHERE run_id=? AND json_extract(state_json,'$.source.provider')=? AND json_extract(state_json,'$.source.thread_id')=? ORDER BY source_id LIMIT 1`, s.RunID, s.Source.Provider, s.Source.ThreadID).Scan(&id)
			if e == nil {
				s.ID = id
				previous, err = getUsageSource(ctx, tx, id)
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if previous.RunID != s.RunID || previous.ConversationID != s.ConversationID || previous.Source.ThreadID != s.Source.ThreadID {
				return protocol.Errorf(protocol.CodeForbidden, "usage source ownership changed")
			}
			if previous.Source.SessionID != "" && s.Source.SessionID != "" && previous.Source.SessionID != s.Source.SessionID {
				return protocol.Errorf(protocol.CodeForbidden, "usage source session changed")
			}
			if s.Source.WriterID == "" || s.Source.WriterID == previous.Collection.WriterID {
				if previous.Source.Path == s.Source.Path {
					return nil
				}
				if s.Source.SessionID == "" {
					s.Source.SessionID = previous.Source.SessionID
				}
				if s.Source.WriterID == "" {
					s.Source.WriterID = previous.Source.WriterID
				}
				if cursor, ok := previous.Cursors[previous.Source.Path]; ok {
					if _, exists := previous.Cursors[s.Source.Path]; !exists {
						previous.Cursors[s.Source.Path] = cursor
					}
				}
				previous.Source = s.Source
				previous.Files = nil
				previous.DiscoveredAt = 0
				return saveUsageSource(ctx, tx, previous, nil, j.now().UnixMilli())
			}
			if !previous.WriterStopped {
				return protocol.Errorf(protocol.CodeStaleRevision, "original usage writer has not stopped")
			}
			previous.Collection.Activate("", s.Source.WriterID)
			if s.Source.SessionID == "" {
				s.Source.SessionID = previous.Source.SessionID
			}
			previous.Source = s.Source
			previous.Files = nil
			previous.DiscoveredAt = 0
			previous.WriterStopped = false
			return replaceUsageCollection(ctx, tx, previous, j.now().UnixMilli())
		}
		if s.Collection.Epoch == 0 {
			s.Collection = protocol.NewUsageCollection(s.Source.NoDispatchProven, s.WriterStopped)
		}
		s.Collection.WriterID = s.Source.WriterID
		if s.WriterStopped && s.Collection.StopProof == "" {
			s.Collection.Stopped(j.now().UnixMilli(), "registered-stopped-writer", s.Source.NoDispatchProven && len(s.Cursors) == 0)
		}
		if s.WriterStopped && s.Coverage == "caught_up" {
			s.Collection.ReadSucceeded(j.now().UnixMilli(), false, true, true, true)
		}
		body, err := json.Marshal(s)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_usage_sources(source_id,run_id,state_json,writer_stopped,collection_state,collection_report_pending,registration_queued) VALUES(?,?,?,?,?,1,?)`, s.ID, s.RunID, string(body), s.WriterStopped, s.Collection.State, s.RegistrationQueued)
		return err
	})
}

func (j *Journal) UsageSources(ctx context.Context) ([]UsageSource, error) {
	return j.usageSources(ctx, false)
}
func (j *Journal) usageSources(ctx context.Context, pending bool) ([]UsageSource, error) {
	query := `SELECT s.state_json,s.writer_stopped OR COALESCE(r.stopped_confirmed,0) FROM native_usage_sources s LEFT JOIN runs r USING(run_id)`
	if pending {
		query += ` WHERE s.collection_state IN ('active','settling') OR s.registration_queued=0`
	}
	rows, err := j.db.QueryContext(ctx, query+` ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSource
	for rows.Next() {
		var raw string
		var stopped bool
		if err = rows.Scan(&raw, &stopped); err != nil {
			return nil, err
		}
		var s UsageSource
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, err
		}
		s.WriterStopped = stopped
		s.ExpectedRevision = s.Collection.Revision
		out = append(out, s)
	}
	return out, rows.Err()
}

// QueueUsage saves the upload and the advanced cursor in a single transaction.
// A crash after this commit replays the original envelope, never skips records.
func (j *Journal) QueueUsage(ctx context.Context, s UsageSource, env *protocol.Envelope) error {
	err := j.tx(ctx, func(tx *sql.Tx) error { return saveUsageSource(ctx, tx, s, env, j.now().UnixMilli()) })
	if err == nil && env != nil {
		j.WakeUsage()
	}
	return err
}

func (j *Journal) HasUsageSource(ctx context.Context, id string) (bool, error) {
	var known bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_usage_sources WHERE source_id=?)`, id).Scan(&known)
	return known, err
}

func (j *Journal) StopUsageSource(ctx context.Context, run, thread string) error {
	defer j.WakeUsage()
	return j.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT source_id FROM native_usage_sources WHERE run_id=? AND json_extract(state_json,'$.source.thread_id')=? AND writer_stopped=0`, run, thread)
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
			s, err := getUsageSource(ctx, tx, id)
			if err != nil {
				return err
			}
			s.WriterStopped = true
			s.Collection.Stopped(j.now().UnixMilli(), "owned-writer-closed:"+s.Collection.WriterID, len(s.Cursors) == 0)
			if err = saveUsageSource(ctx, tx, s, nil, j.now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}

// QueueUsageRegistration promotes retained local evidence into a durable host
// request without rewinding cursors or participating in a run's event sequence.
func (j *Journal) QueueUsageRegistration(ctx context.Context, source UsageSource, env protocol.Envelope) error {
	defer j.WakeUsage()
	return j.tx(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT state_json FROM native_usage_sources WHERE source_id=?`, source.ID).Scan(&raw)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if exists {
			if err := json.Unmarshal([]byte(raw), &source); err != nil {
				return err
			}
			if source.RegistrationQueued {
				return nil
			}
		}
		if source.Collection.Epoch == 0 {
			source.Collection = protocol.NewUsageCollection(source.Source.NoDispatchProven, source.WriterStopped)
		}
		source.RegistrationQueued = true
		body, err := json.Marshal(source)
		if err != nil {
			return err
		}
		if exists {
			_, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET state_json=?,registration_queued=1 WHERE source_id=?`, string(body), source.ID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO native_usage_sources(source_id,run_id,state_json,writer_stopped,collection_state,collection_report_pending,registration_queued) VALUES(?,?,?,?,?,1,1)`, source.ID, source.RunID, string(body), source.WriterStopped, source.Collection.State)
		}
		if err != nil {
			return err
		}
		return putRequest(ctx, tx, Request{Envelope: env}, j.now())
	})
}

// UsageWake has one collector consumer and retains lifecycle notifications.
func (j *Journal) UsageWake() <-chan struct{} { return j.usageWake }
func (j *Journal) WakeUsage() {
	select {
	case j.usageWake <- struct{}{}:
	default:
	}
}
func (j *Journal) PendingUsageSources(ctx context.Context) ([]UsageSource, error) {
	return j.usageSources(ctx, true)
}
func (j *Journal) HasUsageWork(ctx context.Context) (bool, error) {
	var work bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_usage_sources s WHERE s.collection_state IN ('active','settling') OR s.registration_queued=0) OR EXISTS(SELECT 1 FROM requests WHERE operation IN (?,?,?))`, protocol.OpRunnerUsageRegister, protocol.OpRunnerUsageUpload, protocol.OpRunnerUsageCollection).Scan(&work)
	return work, err
}
