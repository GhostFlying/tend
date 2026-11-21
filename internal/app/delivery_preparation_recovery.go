package app

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// recoverDeliveryPreparation releases an unstarted legacy binding after its
// execution stopped. It grants no new candidate, acceptance or installation.
func recoverDeliveryPreparation(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, duty *protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) error {
	task, err := currentLiveDeliveryTask(ctx, tx.Q(), issue.IssueID, duty.TaskID, p.ExpectedTaskControlRevision)
	if err != nil {
		return err
	}
	if duty.ExecutionTaskID != task.TaskID || duty.Status != "executing" || duty.AssignmentEpoch != issue.AssignmentEpoch || duty.Candidate == nil || !duty.Candidate.Deploy {
		return protocol.Errorf(protocol.CodeInvalidTransition, "recover_preparation requires the original unstarted self-bound deployment")
	}
	current, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), task.TaskID)
	if err != nil {
		return err
	}
	if !current || task.ControlMode != protocol.ControlManaged {
		return protocol.Errorf(protocol.CodeStaleRevision, "preparation recovery requires the current managed responsibility")
	}
	var bound, raw string
	if err := tx.Q().QueryRowContext(ctx, `SELECT upgrade_id,candidate_json FROM issue_delivery_executions WHERE task_id=? AND issue_id=? AND source_task_id=? AND duty_revision=?`, task.TaskID, issue.IssueID, task.TaskID, duty.Revision).Scan(&bound, &raw); err != nil {
		return errNotFound(err, "retain the exact original self-bound execution")
	}
	if err := selfDeliveryEffectsReconciled(ctx, tx.Q(), task.TaskID, bound); err != nil {
		return err
	}
	var frozen deliveryExecutionSnapshot
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return err
	}
	if frozen.AssignmentEpoch != issue.AssignmentEpoch || !reflect.DeepEqual(&frozen.IssueDeliveryCandidate, duty.Candidate) || !reflect.DeepEqual(frozen.Authorization, duty.Authorization) {
		return protocol.Errorf(protocol.CodeStaleRevision, "retain the original frozen candidate and authority before preparation recovery")
	}
	if err := store.DeliveryGrantValid(ctx, tx.Q(), issue, *duty); err != nil {
		return err
	}
	if err := stoppedTaskRecoveryReady(ctx, tx.Q(), task.TaskID); err != nil {
		return err
	}
	if _, stage, err := taskPredeploymentResult(ctx, tx.Q(), task); err != nil {
		return err
	} else if stage != nil {
		return protocol.Errorf(protocol.CodeInvalidTransition, "retain the existing typed stage and use its normal candidate continuation")
	}
	if err := archiveSelfDeliveryCandidate(ctx, x, tx, task, *duty); err != nil {
		return err
	}
	duty.Status = "preparing"
	duty.Authorization, duty.ExecutionTaskID = nil, ""
	duty.AcceptanceID, duty.ArtifactID = "", ""
	_, err = tx.Exec(ctx, `UPDATE issue_delivery_duties SET acceptance_id=NULL,artifact_id=NULL WHERE issue_id=? AND task_id=?`, issue.IssueID, task.TaskID)
	return err
}
