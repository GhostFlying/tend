package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DeliveryDeploymentSettlement reads the original completed deployment. Its
// result is an accounting endpoint, never current merge/install authority.
// Expected business failures carry an ineligible projection as well as a
// protocol error; storage failures remain errors and must not become unknown.
func DeliveryDeploymentSettlement(ctx context.Context, q Querier, taskID string, checkedAt int64) (*protocol.DeliverySettlementAuthority, error) {
	var issueID, source, raw, upgradeID string
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT issue_id,source_task_id,duty_revision,candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, taskID).Scan(&issueID, &source, &revision, &raw, &upgradeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var frozen struct {
		protocol.IssueDeliveryCandidate
		AssignmentEpoch int64                        `json:"assignment_epoch"`
		Authorization   *protocol.IssueDeliveryGrant `json:"authorization,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return nil, err
	}
	if !frozen.Deploy {
		return nil, nil
	}
	v := &protocol.DeliverySettlementAuthority{DutyRevision: revision, UpgradeID: upgradeID}
	fail := func(reason string, code protocol.Code, message string) (*protocol.DeliverySettlementAuthority, error) {
		v.InvalidReason = reason
		return v, protocol.Errorf(code, "%s", message)
	}
	issue, err := GetCanonicalIssue(ctx, q, issueID)
	if err != nil {
		return v, err
	}
	duty, err := GetIssueDeliveryDuty(ctx, q, issueID, source)
	if err != nil {
		return v, err
	}
	v.CurrentDutyRevision = duty.Revision
	primary, err := GetTaskIssueOwner(ctx, q, taskID)
	if err != nil {
		return v, err
	}
	if primary != issueID || issue.AssignmentState != "active" || duty.AssignmentEpoch != frozen.AssignmentEpoch {
		return fail("issue_responsibility_changed", protocol.CodeStaleRevision, "the original deployment responsibility changed")
	}
	if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused && issue.State != protocol.IssueCancelling {
		return fail("issue_not_active", protocol.CodeInvalidTransition, "the original Issue is no longer open for settlement")
	}
	// Observation expiry advances the mutable duty version while retaining the
	// execution and its exact contract. An explicit replacement or disposition
	// cannot use that exception.
	if duty.ExecutionTaskID != taskID || duty.Revision < revision || (duty.Status != "executing" && duty.Status != "preparing") || !reflect.DeepEqual(duty.Candidate, &frozen.IssueDeliveryCandidate) || !reflect.DeepEqual(duty.Authorization, frozen.Authorization) {
		return fail("original_execution_replaced", protocol.CodeStaleRevision, "the original delivery execution was replaced or explicitly disposed")
	}
	if frozen.Deployment == nil {
		return fail("deployment_target_missing", protocol.CodeInvalidTransition, "the original deployment has no frozen target")
	}
	if err := DeliveryGrantValid(ctx, q, issue, duty); err != nil {
		v.InvalidReason = "original_authorization_not_current"
		return v, err
	}
	found := false
	for _, ref := range issue.Plan.Deliveries {
		found = found || ref.DeliveryID == frozen.DeliveryID && ref.URL == frozen.URL
	}
	if !found {
		return fail("delivery_not_registered", protocol.CodeStaleRevision, "the original delivery identity is no longer registered on this Issue")
	}
	task, err := GetTask(ctx, q, taskID)
	if err != nil {
		return v, err
	}
	v.ArtifactID = task.CurrentArtifactID
	if task.Lifecycle != protocol.TaskCompleted && task.Kind != protocol.TaskKindIssueManager {
		return fail("execution_not_completed", protocol.CodeInvalidTransition, "the original deployment execution must finish independent acceptance")
	}
	basis, err := CompletedTaskEvidence(ctx, q, taskID, "Original completed delivery result")
	if err != nil {
		v.InvalidReason = "completion_evidence_missing"
		return v, err
	}
	if basis.Kind == protocol.WorkItemEvidenceAcceptance {
		v.AcceptanceID = basis.Reference
	}
	started := task.CreatedAt
	if r := frozen.Reconciliation; r != nil {
		if r.UpgradeID != upgradeID || r.SourceCommit == "" {
			return fail("original_upgrade_changed", protocol.CodeStaleRevision, "verification must retain its exact original installation")
		}
		original, err := GetTask(ctx, q, r.EvidenceTaskID)
		if err != nil {
			return v, err
		}
		started = original.CreatedAt
		v.TargetSHA = r.SourceCommit
	} else if frozen.ProviderState == "merged" {
		v.TargetSHA = frozen.TargetSHA
	}
	// Current target, deployment baseline and refresh scheduling describe new
	// work. Known PR identity conflicts still need reconciliation; a transient
	// provider read does not erase an already recorded merged installation.
	observation, err := GetDeliveryObservation(ctx, q, issueID, frozen.DeliveryID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return v, err
	}
	if err == nil {
		o := observation.Observation
		// A failed later poll preserves the last checked snapshot. Its latest
		// attempt error cannot erase a known conflict in those provider facts.
		if o.CheckedAt != 0 {
			if o.State != "merged" || (o.URL != "" && o.URL != frozen.URL) || o.HeadSHA != frozen.HeadSHA || o.BaseSHA != frozen.BaseSHA || o.MergeSHA == "" || (frozen.MergeSHA != "" && o.MergeSHA != frozen.MergeSHA) {
				return fail("provider_identity_conflict", protocol.CodeStaleRevision, "recorded provider identity conflicts with the original completed deployment")
			}
			if frozen.ProviderState == "open" {
				v.TargetSHA = o.MergeSHA
			}
		}
	}
	proof, err := DeliveryUpgradeEvidence(ctx, q, *frozen.Deployment, upgradeID, started, checkedAt)
	if err != nil {
		v.InvalidReason = "original_installation_unproved"
		if errors.Is(err, ErrNotFound) {
			return fail(v.InvalidReason, protocol.CodeInvalidTransition, "the original controlled upgrade is missing")
		}
		return v, err
	}
	batch, err := GetUpgrade(ctx, q, upgradeID)
	if err != nil {
		return v, err
	}
	if batch.DeliveryTaskID != "" && batch.DeliveryTaskID != taskID && frozen.Reconciliation == nil {
		return fail("original_upgrade_changed", protocol.CodeStaleRevision, "the controlled upgrade belongs to another delivery execution")
	}
	if v.TargetSHA == "" && frozen.ProviderState == "open" && batch.DeliveryTaskID == taskID {
		// Typed upgrade admission already verified the original merge against
		// this execution before freezing its installed source. Do not require
		// a later provider read to reproduce that historical admission.
		v.TargetSHA = proof.SourceCommit
	}
	if v.TargetSHA == "" {
		return fail("original_source_unknown", protocol.CodeUnknownOutcome, "the original merged installation source remains unproved")
	}
	if proof.SourceCommit != v.TargetSHA {
		return fail("original_source_changed", protocol.CodeStaleRevision, "the original installed source does not match the frozen delivery contract")
	}
	v.Upgrade, v.Eligible = proof, true
	return v, nil
}
