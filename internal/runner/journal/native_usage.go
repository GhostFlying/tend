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
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `INSERT INTO native_usage_sources(source_id,run_id,state_json,writer_stopped) VALUES(?,?,?,?) ON CONFLICT(source_id) DO UPDATE SET writer_stopped=MAX(native_usage_sources.writer_stopped,excluded.writer_stopped)`, s.ID, s.RunID, string(body), s.WriterStopped)
	return err
}

func (j *Journal) UsageSources(ctx context.Context) ([]UsageSource, error) {
	return j.usageSources(ctx, false)
}
func (j *Journal) usageSources(ctx context.Context, pending bool) ([]UsageSource, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT s.state_json,s.writer_stopped OR COALESCE(r.stopped_confirmed,0) FROM native_usage_sources s LEFT JOIN runs r USING(run_id) WHERE ?=0 OR COALESCE(json_extract(s.state_json,'$.registration_queued'),0)=0 OR NOT((s.writer_stopped=1 OR COALESCE(r.stopped_confirmed,0)=1) AND COALESCE(json_extract(s.state_json,'$.coverage'),'')='caught_up') ORDER BY source_id`, pending)
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
		out = append(out, s)
	}
	return out, rows.Err()
}

// QueueUsage saves the upload and the advanced cursor in a single transaction.
// A crash after this commit replays the original envelope, never skips records.
func (j *Journal) QueueUsage(ctx context.Context, s UsageSource, env *protocol.Envelope) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(state_json,'$.registration_queued'),0) FROM native_usage_sources WHERE source_id=?`, s.ID).Scan(&s.RegistrationQueued); err != nil {
			return err
		}
		body, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if env != nil {
			if err := putRequest(ctx, tx, Request{Envelope: *env}, j.now()); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET state_json=? WHERE source_id=? AND state_json<>?`, string(body), s.ID, string(body))
		return err
	})
}

func (j *Journal) HasUsageSource(ctx context.Context, id string) (bool, error) {
	var known bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_usage_sources WHERE source_id=?)`, id).Scan(&known)
	return known, err
}

func (j *Journal) StopUsageSource(ctx context.Context, run, thread string) error {
	defer j.WakeUsage()
	_, err := j.db.ExecContext(ctx, `UPDATE native_usage_sources SET writer_stopped=1 WHERE run_id=? AND json_extract(state_json,'$.source.thread_id')=?`, run, thread)
	return err
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
		source.RegistrationQueued = true
		body, err := json.Marshal(source)
		if err != nil {
			return err
		}
		if exists {
			_, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET state_json=? WHERE source_id=?`, string(body), source.ID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO native_usage_sources(source_id,run_id,state_json,writer_stopped) VALUES(?,?,?,?)`, source.ID, source.RunID, string(body), source.WriterStopped)
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
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_usage_sources s LEFT JOIN runs r USING(run_id) WHERE COALESCE(json_extract(s.state_json,'$.registration_queued'),0)=0 OR NOT((s.writer_stopped=1 OR COALESCE(r.stopped_confirmed,0)=1) AND COALESCE(json_extract(s.state_json,'$.coverage'),'')='caught_up')) OR EXISTS(SELECT 1 FROM requests WHERE operation IN (?,?))`, protocol.OpRunnerUsageRegister, protocol.OpRunnerUsageUpload).Scan(&work)
	return work, err
}
