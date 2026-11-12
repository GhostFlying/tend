package app

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func sameDeliveryCandidate(a, b *protocol.IssueDeliveryCandidate) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, right := *a, *b
	// New validation prose alone does not supersede an actual execution.
	left.Validation, right.Validation = "", ""
	return reflect.DeepEqual(left, right)
}

func refreshSelfDeliveryCandidate(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, duty protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) error {
	task, err := currentLiveDeliveryTask(ctx, tx.Q(), issue.IssueID, duty.TaskID, p.ExpectedTaskControlRevision)
	if err != nil {
		return err
	}
	current, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), task.TaskID)
	if err != nil {
		return err
	}
	if !current || task.ControlMode != protocol.ControlManaged || duty.AssignmentEpoch != issue.AssignmentEpoch {
		return protocol.Errorf(protocol.CodeForbidden, "candidate refresh requires the current managed responsibility")
	}
	var raw, bound, source, originalIssue string
	var revision int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT candidate_json,upgrade_id,source_task_id,issue_id,duty_revision FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&raw, &bound, &source, &originalIssue, &revision); err != nil {
		return err
	}
	var frozen deliveryExecutionSnapshot
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return err
	}
	if originalIssue != issue.IssueID || source != task.TaskID || duty.Status != "executing" || revision != duty.Revision || frozen.AssignmentEpoch != issue.AssignmentEpoch || !reflect.DeepEqual(&frozen.IssueDeliveryCandidate, duty.Candidate) {
		return protocol.Errorf(protocol.CodeStaleRevision, "retain the original delivery binding before refreshing its candidate")
	}
	// An installed or uncertain original batch must be reconciled, even when its
	// caller has not yet attached it to the execution. Archived batches belong
	// to their historical candidate, not every subsequent use of this Task.
	var upgrade bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upgrades u WHERE json_extract(u.state_json,'$.delivery_task_id')=? AND NOT EXISTS(SELECT 1 FROM issue_delivery_execution_history h WHERE h.task_id=? AND h.upgrade_id=u.upgrade_id))`, task.TaskID, task.TaskID).Scan(&upgrade); err != nil {
		return err
	}
	if bound != "" || upgrade {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original controlled upgrade before refreshing its candidate")
	}
	var uncertain bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state='unknown') OR
 EXISTS(SELECT 1 FROM run_messages WHERE task_id=? AND status='unknown') OR
 EXISTS(SELECT 1 FROM manager_native_requests n JOIN runs r USING(run_id) WHERE r.task_id=? AND n.status='unknown' AND n.method IN ('start','resume','turn','steer','tool_result','close')) OR
 EXISTS(SELECT 1 FROM hosted_executions e JOIN runs r ON r.run_id=json_extract(e.private_snapshot,'$.runner_run_id') JOIN main_turns t ON t.request_id=e.execution_id WHERE r.task_id=? AND (t.status='unknown' OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)))`, task.TaskID, task.TaskID, task.TaskID, task.TaskID).Scan(&uncertain); err != nil {
		return err
	}
	if uncertain {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original execution outcome before refreshing its candidate")
	}
	observation, err := store.GetDeliveryObservation(ctx, tx.Q(), issue.IssueID, frozen.DeliveryID)
	if err != nil {
		return err
	}
	if frozen.ProviderState != "open" || observation.Observation.State != "open" || observation.Observation.ErrorKind != "" || observation.Observation.RefreshPending || observation.Observation.CheckedAt == 0 {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "verify the original PR remains open before refreshing its candidate")
	}
	if sameDeliveryCandidate(duty.Candidate, p.Candidate) {
		return nil
	}
	return archiveSelfDeliveryCandidate(ctx, x, tx, task, duty)
}
