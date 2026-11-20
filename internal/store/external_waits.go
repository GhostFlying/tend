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

func TaskExternalWaits(ctx context.Context, q Querier, task, runID, status string) ([]protocol.ExternalWait, error) {
	query := `SELECT body_json FROM run_external_waits WHERE task_id=?`
	args := []any{task}
	if runID != "" {
		query += ` AND run_id=?`
		args = append(args, runID)
	}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ExternalWait{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var wait protocol.ExternalWait
		if err := json.Unmarshal([]byte(raw), &wait); err != nil {
			return nil, err
		}
		out = append(out, wait)
	}
	return out, rows.Err()
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

// ApplyChecksWaitObservation shares the exact candidate and provider-result
// rules between a scheduler wake and read-only wait presentation.
func ApplyChecksWaitObservation(v *protocol.ExternalWait, row DeliveryObservationRow) (bool, string) {
	o := row.Observation
	oldHead := v.ObservedHead
	v.ObservedRevision = row.FactRevision
	v.ObservedHead = o.HeadSHA
	v.ObservedBase, v.ObservedTarget = o.BaseSHA, o.TargetSHA
	v.ObservedState = o.State
	if oldHead != "" && o.HeadSHA != "" && oldHead != o.HeadSHA {
		return true, "candidate_changed"
	}
	reason, state := checksWaitTerminal(o)
	v.ObservedState = state
	return reason != "", reason
}

func checksWaitTerminal(o protocol.DeliveryObservation) (reason, state string) {
	if o.State == "closed" || o.State == "merged" {
		return "pull_request_" + o.State, o.State
	}
	if o.CI != nil && o.CI.HeadSHA == o.HeadSHA && !o.RefreshPending && (o.CI.State == "success" || o.CI.State == "failure" || o.CI.State == "cancelled" || o.CI.State == "skipped") {
		return "checks_" + o.CI.State, o.CI.State
	}
	return "", o.State
}

// ChecksWaitEndedAt returns the first observation of the current head or
// terminal condition. A legacy observation can lack that time.
func (r DeliveryObservationRow) ChecksWaitEndedAt(reason string) int64 {
	if reason == "candidate_changed" {
		return r.checksWaitTimes.HeadAt
	}
	return r.checksWaitTimes.TerminalAt
}
