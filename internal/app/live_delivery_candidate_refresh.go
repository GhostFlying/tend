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

func refreshSelfDeliveryCandidate(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, duty *protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) error {
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
	if err := selfDeliveryEffectsReconciled(ctx, tx.Q(), task.TaskID, bound); err != nil {
		return err
	}
	observation, err := store.GetDeliveryObservation(ctx, tx.Q(), issue.IssueID, frozen.DeliveryID)
	if err != nil {
		return err
	}
	o := observation.Observation
	if o.ErrorKind != "" || o.RefreshPending || o.CheckedAt == 0 {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "verify the original PR outcome before refreshing its candidate")
	}
	if frozen.ProviderState != "open" || o.State != "open" {
		if err := mergedPredeploymentContinuation(ctx, tx.Q(), task, frozen.IssueDeliveryCandidate, o, p.Candidate); err != nil {
			return err
		}
	}
	if sameDeliveryCandidate(duty.Candidate, p.Candidate) {
		return nil
	}
	if err := archiveSelfDeliveryCandidate(ctx, x, tx, task, *duty); err != nil {
		return err
	}
	if o.State == "merged" {
		stageID, _, _, err := persistentPredeploymentResult(ctx, tx.Q(), task)
		if err != nil {
			return err
		}
		basis, err := acceptedPredeploymentArtifact(ctx, tx.Q(), task, stageID)
		if err != nil {
			return err
		}
		duty.ArtifactID, duty.AcceptanceID = stageID, ""
		var artifact, acceptance any = stageID, nil
		if basis.Kind == protocol.WorkItemEvidenceAcceptance {
			duty.ArtifactID, duty.AcceptanceID = "", basis.Reference
			artifact, acceptance = nil, basis.Reference
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_delivery_duties SET artifact_id=?,acceptance_id=? WHERE issue_id=? AND task_id=?`, artifact, acceptance, issue.IssueID, task.TaskID); err != nil {
			return err
		}
	}
	return nil
}
