package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
)

func GetExternalWait(ctx context.Context, q Querier, run string) (*protocol.ExternalWait, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM run_external_waits WHERE run_id=?`, run).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var out protocol.ExternalWait
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (t *Tx) PutExternalWait(ctx context.Context, v protocol.ExternalWait, next int64) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_external_waits(run_id,wait_id,wait_key,task_id,status,body_json,next_check_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET status=excluded.status,body_json=excluded.body_json,next_check_at=excluded.next_check_at`, v.RunID, v.WaitID, v.WaitKey, v.TaskID, v.Status, string(raw), next)
	return err
}

func TaskExternalWaits(ctx context.Context, q Querier, task string) ([]protocol.ExternalWait, error) {
	ids, err := FollowupIDs(ctx, q, `SELECT run_id FROM run_external_waits WHERE task_id=? ORDER BY rowid`, task)
	if err != nil {
		return nil, err
	}
	out := []protocol.ExternalWait{}
	for _, id := range ids {
		v, err := GetExternalWait(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// AssignmentExternalWait transmits a stop request and the predecessor's durable
// recovery facts. A missing reusable native thread never blocks new execution.
func AssignmentExternalWait(ctx context.Context, q Querier, as *protocol.Assignment) error {
	v, err := GetExternalWait(ctx, q, as.RunID)
	if err != nil {
		return err
	}
	if v != nil && v.Status == "stopping" {
		as.ExternalWait = v
	}
	var parent string
	err = q.QueryRowContext(ctx, `SELECT run_id FROM run_external_waits WHERE successor_run_id=?`, as.RunID).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	as.ExternalWaitContinuation, err = GetExternalWait(ctx, q, parent)
	if err != nil {
		return err
	}
	old, err := GetRun(ctx, q, parent)
	if err != nil {
		return err
	}
	previous, err := GetRunExecution(ctx, q, parent)
	if errors.Is(err, ErrNotFound) {
		as.ExternalWaitContinuation.ThreadID = ""
		return nil
	}
	if err != nil {
		return err
	}
	current, err := GetRunExecution(ctx, q, as.RunID)
	if errors.Is(err, ErrNotFound) {
		as.ExternalWaitContinuation.ThreadID = ""
		return nil
	}
	if err != nil {
		return err
	}
	next, err := GetRun(ctx, q, as.RunID)
	if err != nil {
		return err
	}
	if old.HostID != next.HostID || !sessionExecutionMatches(previous, current) {
		as.ExternalWaitContinuation.ThreadID = ""
	}
	return nil
}

// AssignmentSessionContinuation chooses only a finished same-Task/role Run
// with independent physical-close proof and matching executor identity. Absence
// of local history or a compatible executor simply starts a fresh session.
func AssignmentSessionContinuation(ctx context.Context, q Querier, as *protocol.Assignment) error {
	if as.Protocol < 3 {
		return nil
	}
	next, err := GetRun(ctx, q, as.RunID)
	if err != nil {
		return err
	}
	var v protocol.SessionContinuation
	err = q.QueryRowContext(ctx, `SELECT prior.run_id,closed.thread_id FROM runs prior JOIN run_session_stops closed USING(run_id) WHERE prior.task_id=? AND prior.role=? AND prior.host_id=? AND prior.run_id<>? AND prior.observed_state='finished' ORDER BY prior.created_at DESC,prior.rowid DESC LIMIT 1`, as.TaskID, as.Role, next.HostID, as.RunID).Scan(&v.RunID, &v.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	old, err := GetRunExecution(ctx, q, v.RunID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := GetRunExecution(ctx, q, as.RunID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sessionExecutionMatches(old, current) {
		as.SessionContinuation = &v
	}
	return nil
}

func sessionExecutionMatches(previous, current *protocol.RunExecutionPin) bool {
	if previous == nil || current == nil {
		return false
	}
	a, b := *previous, *current
	// Another Agent's inventory update does not change this runtime, model,
	// profile, extension policy or actual execution identity.
	a.SnapshotID, b.SnapshotID = "", ""
	a.InventoryRevision, b.InventoryRevision = 0, 0
	a.PublicSHA256, b.PublicSHA256 = "", ""
	return a == b
}
