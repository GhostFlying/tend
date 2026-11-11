package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (j *Journal) Directory() string { return filepath.Dir(j.path) }

func (j *Journal) ManagerOperation(ctx context.Context, run string, p protocol.ManagerNativeRequest) (string, *protocol.ManagerNativeReply, error) {
	if ManagerReadOnly(p.Method) {
		return "received", nil, nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", nil, err
	}
	var status string
	var reply sql.NullString
	err = j.tx(ctx, func(tx *sql.Tx) error {
		stored, err := j.retainNativeBody(ctx, string(raw), true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO manager_native_operations(request_id,run_id,request_json,status) VALUES(?,?,?,'received') ON CONFLICT(request_id) DO NOTHING`, p.ID, run, stored); err != nil {
			return err
		}
		var prior string
		if err := tx.QueryRowContext(ctx, `SELECT request_json,status,reply_json FROM manager_native_operations WHERE request_id=? AND run_id=?`, p.ID, run).Scan(&prior, &status, &reply); err != nil {
			return err
		}
		prior, err = j.readNativeBody(ctx, prior, true)
		if err != nil {
			return err
		}
		var original protocol.ManagerNativeRequest
		if err = json.Unmarshal([]byte(prior), &original); err != nil {
			return err
		}
		normalized, err := json.Marshal(original)
		if err != nil {
			return err
		}
		if string(normalized) != string(raw) {
			return fmt.Errorf("journal: native request identity changed")
		}
		return nil
	})
	if err != nil || !reply.Valid {
		return status, nil, err
	}
	j.spoolMu.RLock()
	defer j.spoolMu.RUnlock()
	var result protocol.ManagerNativeReply
	reply.String, err = j.readNativeBody(ctx, reply.String, false)
	if err != nil {
		return status, nil, err
	}
	err = json.Unmarshal([]byte(reply.String), &result)
	return status, &result, err
}
func (j *Journal) StartManagerOperation(ctx context.Context, id string) error {
	_, err := j.db.ExecContext(ctx, `UPDATE manager_native_operations SET status='started' WHERE request_id=? AND status='received'`, id)
	return err
}
func (j *Journal) CompleteManagerOperation(ctx context.Context, p protocol.ManagerNativeReply) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		stored, err := j.retainNativeBody(ctx, string(raw), false)
		if err != nil {
			return err
		}
		var run, request string
		if err = tx.QueryRowContext(ctx, `SELECT run_id,request_json FROM manager_native_operations WHERE request_id=?`, p.ID).Scan(&run, &request); err != nil {
			return err
		}
		if p.Error == "" {
			request, err = j.readNativeBody(ctx, request, true)
			if err != nil {
				return err
			}
			var original protocol.ManagerNativeRequest
			if err = json.Unmarshal([]byte(request), &original); err != nil {
				return err
			}
			if original.Method == "start" || original.Method == "resume" || original.Method == "fork" {
				var info struct{ ID string }
				if err = json.Unmarshal(p.Result, &info); err != nil {
					return err
				}
				if info.ID != "" {
					var fields map[string]json.RawMessage
					if err = json.Unmarshal(original.Payload, &fields); err != nil {
						return err
					}
					fields["thread_id"], _ = json.Marshal(info.ID)
					original.Method = "resume"
					original.Payload, err = json.Marshal(fields)
					if err != nil {
						return err
					}
					attachment, err := json.Marshal(original)
					if err != nil {
						return err
					}
					retained, err := j.retainNativeBody(ctx, string(attachment), true)
					if err != nil {
						return err
					}
					if _, err = tx.ExecContext(ctx, `INSERT INTO manager_native_attachments(run_id,thread_id,request_json) VALUES(?,?,?) ON CONFLICT(run_id,thread_id) DO UPDATE SET request_json=excluded.request_json`, run, info.ID, retained); err != nil {
						return err
					}
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE manager_native_operations SET status='completed',reply_json=? WHERE request_id=?`, stored, p.ID)
		return err
	})
}

// SaveManagerEvent freezes its sequence and retry envelope in one transaction.
func (j *Journal) SaveManagerEvent(ctx context.Context, run string, env *protocol.Envelope, body json.RawMessage) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO manager_event_sequences(run_id,next_seq) VALUES(?,1) ON CONFLICT(run_id) DO NOTHING`, run); err != nil {
			return err
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT next_seq FROM manager_event_sequences WHERE run_id=?`, run).Scan(&seq); err != nil {
			return err
		}
		payload, err := json.Marshal(protocol.ManagerNativeEvent{Sequence: seq, Body: body})
		if err != nil {
			return err
		}
		env.Payload = payload
		if err = j.putRequest(ctx, tx, Request{RunID: run, Envelope: *env}, j.now()); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE manager_event_sequences SET next_seq=next_seq+1 WHERE run_id=?`, run)
		return err
	})
}

// ManagerAttachments returns confirmed thread identities and their original
// requested settings. Resume only reattaches; it never resends a turn.
func (j *Journal) ManagerAttachments(ctx context.Context, run string) ([]protocol.ManagerNativeRequest, error) {
	j.spoolMu.RLock()
	defer j.spoolMu.RUnlock()
	rows, err := j.db.QueryContext(ctx, `SELECT request_json FROM manager_native_attachments WHERE run_id=? ORDER BY thread_id`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ManagerNativeRequest{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		raw, err = j.readNativeBody(ctx, raw, true)
		if err != nil {
			return nil, err
		}
		var p protocol.ManagerNativeRequest
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (j *Journal) LastManagerEvent(ctx context.Context, run string) (int64, error) {
	var last int64
	err := j.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT next_seq-1 FROM manager_event_sequences WHERE run_id=?),0)`, run).Scan(&last)
	return last, err
}

func (j *Journal) IsManagerClose(ctx context.Context, id string) (bool, error) {
	var method string
	err := j.db.QueryRowContext(ctx, `SELECT json_extract(request_json,'$.method') FROM manager_native_operations WHERE request_id=?`, id).Scan(&method)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return method == "close", err
}

// CompletedManagerClose is the durable proof that no native process needs to
// reopen. Its immutable final event cursor must survive reply-delivery crashes.
func (j *Journal) CompletedManagerClose(ctx context.Context, run string) (*protocol.ManagerNativeReply, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT reply_json FROM manager_native_operations
		WHERE run_id=? AND status='completed' AND json_extract(request_json,'$.method')='close'
		AND COALESCE(json_extract(reply_json,'$.error'),'')='' ORDER BY rowid DESC LIMIT 1`, run).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var reply protocol.ManagerNativeReply
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// ManagerReadOnly identifies native observations whose repetition cannot launch
// or mutate work. These RPCs have no durable execution command or result cache.
func ManagerReadOnly(method string) bool {
	return method == "models" || method == "find" || method == "usage"
}
