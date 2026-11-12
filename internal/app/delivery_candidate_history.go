package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The caller verifies that this live candidate has no unsettled external effect.
// Supersession preserves its real facts without claiming a completed delivery.
func archiveSelfDeliveryCandidate(ctx context.Context, x *Exec, tx *store.Tx, task store.TaskRow, duty protocol.IssueDeliveryDuty) error {
	var issue, source, candidate, upgrade string
	var revision int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT issue_id,source_task_id,duty_revision,candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&issue, &source, &revision, &candidate, &upgrade); err != nil {
		return err
	}
	if source != task.TaskID || duty.TaskID != source || duty.ExecutionTaskID != task.TaskID || duty.Revision != revision || duty.Status != "executing" || upgrade != "" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "retain and reconcile the original delivery binding before changing its candidate")
	}
	var run, artifact sql.NullString
	var generation int64
	if duty.ArtifactID != "" {
		artifact = sql.NullString{String: duty.ArtifactID, Valid: true}
		if err := tx.Q().QueryRowContext(ctx, `SELECT r.run_id,r.generation FROM artifacts a JOIN runs r USING(run_id) WHERE a.artifact_id=? AND a.task_id=?`, artifact.String, task.TaskID).Scan(&run, &generation); err != nil {
			return err
		}
	} else {
		if x.coordinationExecution != nil {
			actual := x.coordinationExecution.Selection.RunnerRunID
			err := tx.Q().QueryRowContext(ctx, `SELECT run_id,generation FROM runs WHERE run_id=? AND task_id=?`, actual, task.TaskID).Scan(&run, &generation)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		// This is snapshot provenance, not an invented result producer. Before
		// the first actual Run the reference remains NULL and generation zero.
		// A coordinator may update another Task in the same Issue. Its caller
		// Run must not become that target's execution provenance.
		if !run.Valid {
			err := tx.Q().QueryRowContext(ctx, `SELECT run_id,generation FROM runs WHERE task_id=? ORDER BY generation DESC,created_at DESC,run_id DESC LIMIT 1`, task.TaskID).Scan(&run, &generation)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	raw, err := json.Marshal(duty)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_execution_history(task_id,generation,duty_revision,run_id,issue_id,source_task_id,candidate_json,duty_json,artifact_id,upgrade_id,request_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, task.TaskID, generation, revision, run, issue, source, candidate, string(raw), artifact, upgrade, x.Env.RequestID, x.Now.UnixMilli()); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM issue_delivery_executions WHERE task_id=?`, task.TaskID)
	return err
}
