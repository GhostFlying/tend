package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Archive only a settled execution before a same-goal Task continuation or a
// continuing owner's new result. The owner may still be using its real Run;
// this does not settle or retry the old candidate, or clear unknown effects.
func archiveTaskDeliveryForContinuation(ctx context.Context, x *Exec, tx *store.Tx, task store.TaskRow) error {
	var issue, source, candidate, upgrade string
	var revision int64
	err := tx.Q().QueryRowContext(ctx, `SELECT issue_id,source_task_id,duty_revision,candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&issue, &source, &revision, &candidate, &upgrade)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	duty, err := store.GetIssueDeliveryDuty(ctx, tx.Q(), issue, source)
	if err != nil {
		return err
	}
	if duty.Status != "complete" && duty.Status != "report" && duty.Status != "ended" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "settle the previous delivery responsibility before continuing its Task")
	}
	if upgrade != "" {
		batch, err := store.GetUpgrade(ctx, tx.Q(), upgrade)
		if err != nil {
			return err
		}
		if !settledDeliveryUpgrade(batch) {
			failure := protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original controlled upgrade before continuing the Task")
			if batch.View.Status == "cancelled" {
				pending := []string{}
				for _, host := range batch.Hosts {
					if !host.CancellationConfirmed {
						pending = append(pending, host.HostID)
					}
				}
				failure.Details, _ = json.Marshal(map[string]any{"upgrade_id": batch.View.UpgradeID, "status": batch.View.Status, "server_instance": batch.ServerInstance, "server_cancellation_pending": !batch.ServerCancellationConfirmed, "pending_host_ids": pending})
			}
			return failure
		}
	}
	var run string
	var generation int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT r.run_id,r.generation FROM artifacts a JOIN runs r USING(run_id) WHERE a.artifact_id=? AND a.task_id=?`, task.CurrentArtifactID, task.TaskID).Scan(&run, &generation); err != nil {
		return err
	}
	raw, err := json.Marshal(duty)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_execution_history(task_id,generation,duty_revision,run_id,issue_id,source_task_id,candidate_json,duty_json,artifact_id,upgrade_id,request_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, task.TaskID, generation, revision, run, issue, source, candidate, string(raw), task.CurrentArtifactID, upgrade, x.Env.RequestID, x.Now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM issue_delivery_executions WHERE task_id=?`, task.TaskID); err != nil {
		return err
	}
	if source == task.TaskID {
		_, err = tx.Exec(ctx, `DELETE FROM issue_delivery_duties WHERE issue_id=? AND task_id=?`, issue, task.TaskID)
	}
	return err
}

func settledDeliveryUpgrade(batch store.UpgradeRow) bool {
	if batch.View.Status == "completed" {
		return true
	}
	if batch.View.Status != "cancelled" || !batch.ServerCancellationConfirmed {
		return false
	}
	for _, host := range batch.Hosts {
		if !host.CancellationConfirmed {
			return false
		}
	}
	return true
}
