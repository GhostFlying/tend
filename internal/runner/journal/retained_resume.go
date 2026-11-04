package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RetainedStop is independent host-authenticated control, not attachment or
// business success. The digest binds it to the unchanged local assignment.
type RetainedStop struct {
	HelloRequestID   string `json:"hello_request_id"`
	AssignmentSHA256 string `json:"assignment_sha256"`
}

type retainedResumeRecord struct {
	protocol.RetainedRunResume
	Stop *RetainedStop `json:"independent_stop,omitempty"`
}

func assignmentDigest(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }

// AuthorizeRetainedStop records only the server's explicit Stop for the held
// journal. Ordinary runs do not need a strict settlement exception.
func (j *Journal) AuthorizeRetainedStop(ctx context.Context, id, helloRequestID string) error {
	if helloRequestID == "" {
		return errors.New("journal: Stop receipt has no Hello identity")
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var raw, assignment string
		err := tx.QueryRowContext(ctx, `SELECT a.authority_json,r.assignment_json FROM retained_run_resumes a JOIN runs r USING(run_id) WHERE run_id=? AND r.phase<>'done'`, id).Scan(&raw, &assignment)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var state retainedResumeRecord
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return err
		}
		state.Stop = &RetainedStop{HelloRequestID: helloRequestID, AssignmentSHA256: assignmentDigest(assignment)}
		body, err := json.Marshal(state)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE retained_run_resumes SET authority_json=? WHERE run_id=?`, string(body), id)
		return err
	})
}

func (j *Journal) RetainedStop(ctx context.Context, id string) (*RetainedStop, error) {
	var raw, assignment string
	err := j.db.QueryRowContext(ctx, `SELECT a.authority_json,r.assignment_json FROM retained_run_resumes a JOIN runs r USING(run_id) WHERE run_id=?`, id).Scan(&raw, &assignment)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state retainedResumeRecord
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	if state.Stop == nil || state.Stop.HelloRequestID == "" || state.Stop.AssignmentSHA256 != assignmentDigest(assignment) {
		return nil, nil
	}
	return state.Stop, nil
}

func (j *Journal) ClearRetainedStop(ctx context.Context, id string) error {
	return j.exec(ctx, `UPDATE retained_run_resumes SET authority_json=json_remove(authority_json,'$.independent_stop') WHERE run_id=?`, id)
}

// archiveRetainedStop preserves exact pending facts before independent Stop
// removes them from scheduling queues. It does not claim they were applied.
func archiveRetainedStop(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE retained_run_resumes SET authority_json=json_set(authority_json,
	 '$.stop_history',json_object(
	  'requests',json((SELECT json_group_array(json_object('request_id',request_id,'operation',operation,'run_id',run_id,'target_json',target_json,'payload_json',payload_json,'created_at',created_at,'expected_control_revision',expected_control_revision)) FROM requests WHERE run_id=?)),
	  'events',json((SELECT json_group_array(json_object('seq',seq,'native_id',native_id,'kind',kind,'body_json',body_json)) FROM events WHERE run_id=?))))
	 WHERE run_id=? AND json_extract(authority_json,'$.independent_stop.hello_request_id') IS NOT NULL AND json_extract(authority_json,'$.stop_history') IS NULL`, id, id, id)
	return err
}

func (j *Journal) SetRetainedResume(ctx context.Context, id string, p protocol.RetainedRunResume) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return j.exec(ctx, `INSERT INTO retained_run_resumes(run_id,authority_json) VALUES(?,?) ON CONFLICT(run_id) DO UPDATE SET authority_json=excluded.authority_json`, id, string(b))
}
func (j *Journal) RetainedResume(ctx context.Context, id string) (*protocol.RetainedRunResume, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p protocol.RetainedRunResume
	err = json.Unmarshal([]byte(raw), &p)
	return &p, err
}
