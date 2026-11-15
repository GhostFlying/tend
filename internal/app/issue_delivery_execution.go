package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Final reports retain their actual upgrade reference. Candidate stages are
// explicitly separate and cannot bind or settle an installation. Bind a real
// upgrade atomically before acceptance can race the first maintenance tick.
func bindDeliveryUpgradeArtifact(ctx context.Context, tx *store.Tx, task store.TaskRow, artifact protocol.ArtifactPayload, runID string, now int64) error {
	if artifact.Report != nil && artifact.Report.Predeployment != nil {
		return validatePredeploymentResult(ctx, tx.Q(), task, artifact, runID, now)
	}
	var raw, bound string
	err := tx.Q().QueryRowContext(ctx, `SELECT candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&raw, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var candidate protocol.IssueDeliveryCandidate
	if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
		return err
	}
	if !candidate.Deploy {
		return nil
	}
	if artifact.Report == nil || candidate.Deployment == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "delivery requires its deployment report and approved target")
	}
	id, err := deliveryUpgradeReference(artifact.Report)
	if err != nil {
		return err
	}
	if bound != "" && bound != id {
		return protocol.Errorf(protocol.CodeInvalidRequest, "report must retain the original controlled upgrade")
	}
	started := task.CreatedAt
	if candidate.Reconciliation != nil {
		if id != candidate.Reconciliation.UpgradeID {
			return protocol.Errorf(protocol.CodeInvalidRequest, "verification must retain the completed upgrade")
		}
		var issue string
		if err := tx.Q().QueryRowContext(ctx, `SELECT issue_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&issue); err != nil {
			return err
		}
		_, at, err := checkDeliveryReconciliation(ctx, tx.Q(), issue, &candidate)
		if err != nil {
			return err
		}
		started = at
	}
	if _, err := deliveryUpgradeBinding(ctx, tx.Q(), *candidate.Deployment, id, started); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=?`, id, task.TaskID)
	return err
}

// A deployed delivery's reviewer must not retain a run on a target while the
// installer is draining it. Terminal failures remain independently reviewable;
// only verified successful installations can later settle the delivery duty.
func deliveryAcceptanceReady(ctx context.Context, q store.Querier, task string) (bool, error) {
	var raw, id string
	err := q.QueryRowContext(ctx, `SELECT candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task).Scan(&raw, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var candidate protocol.IssueDeliveryCandidate
	if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
		return false, err
	}
	if !candidate.Deploy {
		return true, nil
	}
	available, err := deliveryTargetAvailable(ctx, q, task)
	if err != nil || !available {
		return false, err
	}
	if taskRow, err := store.GetTask(ctx, q, task); err != nil {
		return false, err
	} else if _, stage, err := taskPredeploymentResult(ctx, q, taskRow); err != nil {
		return false, err
	} else if stage != nil {
		// Independent acceptance judges this candidate stage. The original
		// deployment duty remains executing and requires a later final report.
		return true, nil
	}
	if id == "" {
		return false, nil
	}
	batch, err := store.GetUpgrade(ctx, q, id)
	if err != nil {
		return false, err
	}
	return batch.View.Status != "running" && batch.View.Status != "attention", nil
}
