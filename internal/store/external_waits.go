package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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

// The board evidence is private to an original checks wait. Rereading or
// updating an ExternalWait must preserve the already recorded provider fact.
type checksWaitBoardEvidence struct {
	Head          string `json:"head,omitempty"`
	InvalidatedAt *int64 `json:"invalidated_at,omitempty"`
	FactRevision  int64  `json:"fact_revision,omitempty"`
}

func (t *Tx) PutExternalWait(ctx context.Context, v protocol.ExternalWait, next int64) error {
	stored := struct {
		protocol.ExternalWait
		BoardChecks *checksWaitBoardEvidence `json:"board_checks,omitempty"`
	}{ExternalWait: v}
	if v.Kind == "checks" && v.Status == "stopping" {
		stored.BoardChecks = &checksWaitBoardEvidence{Head: v.ObservedHead}
		if v.ObservedState == "closed" || v.ObservedState == "merged" || checksTerminal(v.ObservedState) {
			// An already ended condition could not become a physically stopped wait.
			stored.BoardChecks.InvalidatedAt = new(int64)
			stored.BoardChecks.FactRevision = v.ObservedRevision
		}
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_external_waits(run_id,wait_id,wait_key,task_id,status,body_json,next_check_at)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET status=excluded.status,next_check_at=excluded.next_check_at,
 body_json=CASE WHEN json_type(run_external_waits.body_json,'$.board_checks')='object'
  THEN json_set(excluded.body_json,'$.board_checks',json_extract(run_external_waits.body_json,'$.board_checks'))
  ELSE json_remove(excluded.body_json,'$.board_checks') END`, v.RunID, v.WaitID, v.WaitKey, v.TaskID, v.Status, string(raw), next)
	return err
}

// RecordChecksWaitBoardEvidence runs in the provider result transaction after
// settling the query. Read through the same refresh fence as the wake check.
func (t *Tx) RecordChecksWaitBoardEvidence(ctx context.Context, ref protocol.IssueDelivery) error {
	fact, err := GetDeliveryObservationFact(ctx, t.Q(), ref)
	if err != nil {
		return err
	}
	observation := fact.Observation
	reason, _ := checksWaitTerminal(observation)
	if observation.RefreshPending || (observation.HeadSHA == "" && reason == "") {
		return nil
	}
	// A failed read can retain a prior snapshot and settle the refresh that
	// fenced it. The condition becomes effective when that query settles.
	observedAt := max(observation.CheckedAt, observation.AttemptedAt)
	_, err = t.Exec(ctx, `
WITH relevant AS (
 SELECT w.run_id,COALESCE(json_extract(w.body_json,'$.board_checks.head'),'') AS head
 FROM run_external_waits w JOIN server_delivery_references r
 ON r.issue_id=json_extract(w.body_json,'$.issue_id') AND r.delivery_id=json_extract(w.body_json,'$.target_id')
 WHERE w.status IN ('stopping','waiting') AND r.provider=:provider AND r.repository=:repository AND r.number=:number
 AND json_type(w.body_json,'$.board_checks')='object'
 AND json_extract(w.body_json,'$.board_checks.invalidated_at') IS NULL
),
updates AS (
 SELECT run_id,COALESCE(NULLIF(head,''),:head) AS head,
   :terminal OR (head<>'' AND :head<>'' AND head<>:head) AS invalidated
 FROM relevant WHERE :terminal OR (:head<>'' AND head<>:head)
)
UPDATE run_external_waits AS w SET body_json=json_set(w.body_json,'$.board_checks',
 json_object('head',u.head,'invalidated_at',CASE WHEN u.invalidated THEN :observed_at END,
  'fact_revision',CASE WHEN u.invalidated THEN :fact_revision END))
FROM updates u WHERE w.run_id=u.run_id`,
		sql.Named("provider", ref.Provider), sql.Named("repository", ref.Repository), sql.Named("number", ref.Number),
		sql.Named("head", observation.HeadSHA), sql.Named("terminal", reason != ""), sql.Named("observed_at", observedAt),
		sql.Named("fact_revision", fact.FactRevision))
	if err != nil {
		return fmt.Errorf("store: record checks wait board evidence: %w", err)
	}
	return nil
}

func TaskExternalWaits(ctx context.Context, q Querier, task, runID, status string) ([]protocol.ExternalWait, error) {
	base := `SELECT body_json, rowid AS sequence, 0 AS immediate FROM run_external_waits WHERE task_id=?`
	args := []any{task}
	if runID != "" {
		base += ` AND run_id=?`
		args = append(args, runID)
	}
	if status != "" {
		base += ` AND status=?`
		args = append(args, status)
	}
	if status == "" || status == protocol.ExternalWaitSatisfied {
		immediate := `SELECT json_extract(e.body_json,'$.wait') AS body_json,e.event_seq AS sequence,1 AS immediate
 FROM runs r JOIN events e ON e.object_type='run' AND e.object_id=r.run_id WHERE r.task_id=? AND e.kind=?`
		args = append(args, task, protocol.EventPRWaitSatisfied)
		if runID != "" {
			immediate += ` AND r.run_id=?`
			args = append(args, runID)
		}
		base += ` UNION ALL ` + immediate
	}
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM (`+base+`) ORDER BY json_extract(body_json,'$.created_at_ms'),immediate,sequence`, args...)
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
	if o.CI != nil && o.CI.HeadSHA == o.HeadSHA && !o.RefreshPending && checksTerminal(o.CI.State) {
		return "checks_" + o.CI.State, o.CI.State
	}
	return "", o.State
}

func checksTerminal(state string) bool {
	switch state {
	case "success", "failure", "cancelled", "skipped":
		return true
	default:
		return false
	}
}
