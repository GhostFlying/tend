package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A candidate result is useful before installation, but it cannot clear an
// unknown external operation or borrow another Task's delivery authority.
func selfDeliveryEffectsReconciled(ctx context.Context, q store.Querier, task, bound string) error {
	// An installed or uncertain original batch must be reconciled, even when its
	// caller has not yet attached it to the execution. Archived batches belong
	// to their historical candidate, not every subsequent use of this Task.
	var upgrade bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upgrades u WHERE json_extract(u.state_json,'$.delivery_task_id')=? AND NOT EXISTS(SELECT 1 FROM issue_delivery_execution_history h WHERE h.task_id=? AND h.upgrade_id=u.upgrade_id))`, task, task).Scan(&upgrade); err != nil {
		return err
	}
	if bound != "" || upgrade {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original controlled upgrade before refreshing its candidate")
	}
	var uncertain bool
	if err := q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state='unknown') OR
 EXISTS(SELECT 1 FROM run_messages WHERE task_id=? AND status='unknown') OR
 EXISTS(SELECT 1 FROM manager_native_requests n JOIN runs r USING(run_id) WHERE r.task_id=? AND n.status='unknown' AND n.method IN ('start','resume','turn','steer','tool_result','close')) OR
 EXISTS(SELECT 1 FROM hosted_executions e JOIN runs r ON r.run_id=json_extract(e.private_snapshot,'$.runner_run_id') JOIN main_turns t ON t.request_id=e.execution_id WHERE r.task_id=? AND (t.status='unknown' OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)))`, task, task, task, task).Scan(&uncertain); err != nil {
		return err
	}
	if uncertain {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original execution outcome before refreshing its candidate")
	}

	return nil
}

func taskPredeploymentResult(ctx context.Context, q store.Querier, task store.TaskRow) (protocol.ArtifactPayload, *protocol.PredeploymentResult, error) {
	var payload protocol.ArtifactPayload
	if task.CurrentArtifactID == "" {
		return payload, nil, nil
	}
	artifact, err := store.GetArtifact(ctx, q, task.CurrentArtifactID)
	if err != nil {
		return payload, nil, err
	}
	if err := json.Unmarshal([]byte(artifact.RefJSON), &payload); err != nil {
		return payload, nil, err
	}
	if payload.Report == nil {
		return payload, nil, nil
	}
	return payload, payload.Report.Predeployment, nil
}

func validatePredeploymentResult(ctx context.Context, q store.Querier, task store.TaskRow, artifact protocol.ArtifactPayload, runID string, now int64) error {
	stage := artifact.Report.Predeployment
	if stage.DeliveryID == "" || !shaPattern.MatchString(stage.HeadSHA) || !shaPattern.MatchString(stage.BaseSHA) || strings.TrimSpace(artifact.Report.Body) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "predeployment requires the exact candidate and readable report")
	}
	for _, e := range artifact.Report.Evidence {
		if strings.HasPrefix(e, "upgrade:") {
			return protocol.Errorf(protocol.CodeInvalidRequest, "predeployment cannot claim an upgrade")
		}
	}
	run, err := store.GetRun(ctx, q, runID)
	if err != nil {
		return err
	}
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return err
	}
	if run.TaskID != task.TaskID || task.Kind != protocol.TaskKindIssueManager && (action.TargetSpecVersion != task.SpecVersion || run.Generation != task.ImplementGeneration) {
		return protocol.Errorf(protocol.CodeStaleRevision, "predeployment requires the current producing Task execution")
	}
	if task.Kind == protocol.TaskKindIssueManager {
		allowed, err := managerNativeBusinessAllowed(ctx, q, run)
		if err != nil {
			return err
		}
		if !allowed {
			return protocol.Errorf(protocol.CodeForbidden, "predeployment requires the actual current Issue owner execution")
		}
	}
	lease, err := store.GetLeaseByRun(ctx, q, runID)
	if err != nil {
		return err
	}
	if lease.Status != store.LeaseActive || lease.ExpiresAt <= now || run.DesiredState != protocol.DesiredRun || (run.ObservedState != protocol.RunRunning && run.ObservedState != protocol.RunStarting) || task.ControlMode != protocol.ControlManaged {
		return protocol.Errorf(protocol.CodeForbidden, "predeployment requires a current active execution lease")
	}
	var issueID, source, raw, upgrade string
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT issue_id,source_task_id,candidate_json,upgrade_id,duty_revision FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&issueID, &source, &raw, &upgrade, &revision); err != nil {
		return errNotFound(err, "predeployment requires an existing self-bound delivery")
	}
	var frozen deliveryExecutionSnapshot
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, issueID)
	if err != nil {
		return err
	}
	duty, err := store.GetIssueDeliveryDuty(ctx, q, issueID, source)
	if err != nil {
		return err
	}
	if source != task.TaskID || !frozen.Deploy || frozen.Reconciliation != nil || duty.ExecutionTaskID != task.TaskID || duty.Status != "executing" || duty.AssignmentEpoch != issue.AssignmentEpoch || frozen.AssignmentEpoch != issue.AssignmentEpoch {
		return protocol.Errorf(protocol.CodeForbidden, "predeployment must retain its original self-bound deployment responsibility")
	}
	if revision != duty.Revision || !reflect.DeepEqual(duty.Candidate, &frozen.IssueDeliveryCandidate) || !reflect.DeepEqual(duty.Authorization, frozen.Authorization) {
		return protocol.Errorf(protocol.CodeStaleRevision, "retain the original frozen delivery binding")
	}
	current, err := store.TaskOwnerContractCurrent(ctx, q, task.TaskID)
	if err != nil {
		return err
	}
	if !current {
		return protocol.Errorf(protocol.CodeStaleRevision, "predeployment requires the current Issue contract")
	}
	if err := store.DeliveryGrantValid(ctx, q, issue, duty); err != nil {
		return err
	}
	if err := selfDeliveryEffectsReconciled(ctx, q, task.TaskID, upgrade); err != nil {
		return err
	}
	original, err := store.GetDeliveryObservation(ctx, q, issueID, frozen.DeliveryID)
	if err != nil {
		return err
	}
	observed, err := store.GetDeliveryObservation(ctx, q, issueID, stage.DeliveryID)
	if err != nil {
		return err
	}
	o := observed.Observation
	if o.ErrorKind != "" || o.RefreshPending || o.CheckedAt == 0 || (o.State != "open" && o.State != "merged") || o.HeadSHA != stage.HeadSHA || o.BaseSHA != stage.BaseSHA {
		return protocol.Errorf(protocol.CodeStaleRevision, "predeployment candidate must match the recorded provider identity")
	}
	// A registered reference is not permission to submit a different repository.
	var oldRepo, nextRepo, oldProvider, nextProvider string
	if err := q.QueryRowContext(ctx, `SELECT provider,repository FROM server_delivery_references WHERE issue_id=? AND delivery_id=?`, issueID, frozen.DeliveryID).Scan(&oldProvider, &oldRepo); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT provider,repository FROM server_delivery_references WHERE issue_id=? AND delivery_id=?`, issueID, stage.DeliveryID).Scan(&nextProvider, &nextRepo); err != nil {
		return err
	}
	if oldProvider != nextProvider || oldRepo != nextRepo {
		return protocol.Errorf(protocol.CodeForbidden, "predeployment must retain the authorized repository")
	}
	old := original.Observation
	if old.ErrorKind != "" || old.RefreshPending || old.CheckedAt == 0 || (old.State != "open" && old.State != "merged") || old.HeadSHA != frozen.HeadSHA || old.BaseSHA != frozen.BaseSHA {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original candidate before recording a new stage")
	}
	matched := false
	for _, mount := range artifact.Mounts {
		matched = matched || mount.CommitSHA == stage.HeadSHA && mount.SnapshotSHA == ""
	}
	if !matched {
		return protocol.Errorf(protocol.CodeInvalidRequest, "predeployment must include the candidate's committed repository state")
	}
	return nil
}

func acceptedPredeploymentResult(ctx context.Context, q store.Querier, task store.TaskRow, stage *protocol.PredeploymentResult) (protocol.IssueEvidence, error) {
	if stage == nil {
		return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "the new candidate requires its actual predeployment result")
	}
	return acceptedPredeploymentArtifact(ctx, q, task, task.CurrentArtifactID)
}

func acceptedPredeploymentArtifact(ctx context.Context, q store.Querier, task store.TaskRow, artifactID string) (protocol.IssueEvidence, error) {
	_, stage, err := readPredeploymentArtifact(ctx, q, task, artifactID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	if stage == nil {
		return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "the candidate requires a real typed stage")
	}
	if task.Kind == protocol.TaskKindIssueManager {
		passed, err := store.OwnerResultEvidence(ctx, q, artifactID)
		if err != nil {
			return protocol.IssueEvidence{}, err
		}
		if !passed {
			return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "the actual owner stage needs its independent review")
		}
		return protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceResult, Reference: artifactID}, nil
	}
	accs, err := store.ListAcceptances(ctx, q, task.TaskID, artifactID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	for _, a := range accs {
		if a.SpecVersion == task.SpecVersion && a.Verdict == protocol.VerdictPassed {
			return protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceAcceptance, Reference: a.AcceptanceID}, nil
		}
	}
	policy, err := store.TaskAcceptancePolicy(ctx, q, task.TaskID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	if policy != nil && !policy.Required {
		return protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceResult, Reference: artifactID}, nil
	}
	return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "the candidate stage requires its frozen independent acceptance")
}

// This is scheduling permission for a preparation Run, never installation
// permission. The controlled upgrade boundary keeps its exact-source checks.
func predeploymentPreparationCurrent(ctx context.Context, q store.Querier, issue protocol.Issue, duty protocol.IssueDeliveryDuty, taskID string, revision int64) (bool, error) {
	task, err := store.GetTask(ctx, q, taskID)
	if err != nil {
		return false, err
	}
	stageID, stage, rejected, err := preparationStage(ctx, q, task)
	if err != nil || stage == nil || duty.Candidate == nil {
		return false, err
	}
	observed, err := store.GetDeliveryObservation(ctx, q, issue.IssueID, duty.Candidate.DeliveryID)
	if err != nil {
		return false, err
	}
	o := observed.Observation
	if o.ErrorKind == "" && !o.RefreshPending && o.State == "open" {
		// The ordinary reviewed open-candidate path remains unchanged.
		return false, nil
	}
	if duty.TaskID != taskID || duty.ExecutionTaskID != taskID || duty.Status != "executing" || duty.Revision != revision || duty.AssignmentEpoch != issue.AssignmentEpoch || !duty.Candidate.Deploy || duty.Candidate.Reconciliation != nil {
		return false, protocol.Errorf(protocol.CodeStaleRevision, "retain the original deployment responsibility for preparation")
	}
	if err := store.DeliveryGrantValid(ctx, q, issue, duty); err != nil {
		return false, err
	}
	if current, err := store.TaskOwnerContractCurrent(ctx, q, taskID); err != nil || !current {
		if err != nil {
			return false, err
		}
		return false, protocol.Errorf(protocol.CodeStaleRevision, "preparation requires the current Issue contract")
	}
	if !rejected {
		if _, err := acceptedPredeploymentArtifact(ctx, q, task, stageID); err != nil {
			return false, err
		}
	}
	if err := selfDeliveryEffectsReconciled(ctx, q, taskID, ""); err != nil {
		return false, err
	}
	if o.ErrorKind != "" || o.RefreshPending || o.CheckedAt == 0 || o.State != "merged" || o.MergeSHA == "" || !validDeliverySHA(o.TargetSHA) || o.LineageErrorKind != "" || !o.TargetContainsMerge || o.HeadSHA != duty.Candidate.HeadSHA || o.BaseSHA != duty.Candidate.BaseSHA {
		return false, protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original merge before preparing its next candidate")
	}
	return true, nil
}

func sameDeploymentHosts(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return len(a) > 0 && len(slices.Compact(slices.Clone(a))) == len(a) && len(slices.Compact(slices.Clone(b))) == len(b) && slices.Equal(a, b)
}

// Commit output already independently accepted for this exact candidate need
// not be manufactured again by its installation Run. Preserve Changed=false;
// this exception cannot authorize an ordinary unchanged commit result.
func unchangedDeliveryMountsAllowed(ctx context.Context, q store.Querier, runID string, result protocol.ArtifactPayload) (bool, error) {
	if result.Report == nil {
		return false, nil
	}
	if result.Report.Predeployment != nil {
		// Submission still validates the complete typed stage, including the
		// actual producing lease, frozen authority and recorded PR identity.
		return true, nil
	}
	id, err := deliveryUpgradeReference(result.Report)
	if err != nil {
		return false, nil
	}
	run, err := store.GetRun(ctx, q, runID)
	if err != nil {
		return false, err
	}
	task, err := store.GetTask(ctx, q, run.TaskID)
	if err != nil {
		return false, err
	}
	var issueID, source, raw, bound string
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT issue_id,source_task_id,duty_revision,candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&issueID, &source, &revision, &raw, &bound); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var frozen deliveryExecutionSnapshot
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return false, err
	}
	accepted, stage, err := acceptedStageForCandidate(ctx, q, task, frozen.IssueDeliveryCandidate)
	if err != nil || stage == nil {
		return false, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, issueID)
	if err != nil {
		return false, err
	}
	duty, err := store.GetIssueDeliveryDuty(ctx, q, issueID, source)
	if err != nil {
		return false, err
	}
	if source != task.TaskID || !frozen.Deploy || frozen.Deployment == nil || frozen.Reconciliation != nil || stage.DeliveryID != frozen.DeliveryID || stage.HeadSHA != frozen.HeadSHA || stage.BaseSHA != frozen.BaseSHA || duty.Status != "executing" || duty.ExecutionTaskID != task.TaskID || duty.Revision != revision || duty.AssignmentEpoch != issue.AssignmentEpoch || frozen.AssignmentEpoch != issue.AssignmentEpoch || !reflect.DeepEqual(duty.Candidate, &frozen.IssueDeliveryCandidate) || !reflect.DeepEqual(duty.Authorization, frozen.Authorization) {
		return false, protocol.Errorf(protocol.CodeStaleRevision, "unchanged installation output requires its exact accepted candidate and frozen deployment")
	}
	if current, err := store.TaskOwnerContractCurrent(ctx, q, task.TaskID); err != nil || !current {
		if err != nil {
			return false, err
		}
		return false, protocol.Errorf(protocol.CodeStaleRevision, "installation output requires the current Issue contract")
	}
	if err := store.DeliveryGrantValid(ctx, q, issue, duty); err != nil {
		return false, err
	}
	if bound != "" && bound != id {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "retain the original controlled upgrade")
	}
	if _, err := deliveryUpgradeBinding(ctx, q, *frozen.Deployment, id, task.CreatedAt); err != nil {
		return false, err
	}
	if len(result.Mounts) != len(accepted.Mounts) {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "retain every accepted repository state")
	}
	byPath := make(map[string]protocol.ArtifactMount, len(accepted.Mounts))
	for _, mount := range accepted.Mounts {
		byPath[mount.Path] = mount
	}
	for _, mount := range result.Mounts {
		original, ok := byPath[mount.Path]
		if !ok || original.Repo != mount.Repo || original.BaseSHA != mount.BaseSHA || original.CommitSHA != mount.CommitSHA || original.SnapshotSHA != "" || mount.SnapshotSHA != "" {
			return false, protocol.Errorf(protocol.CodeInvalidRequest, "installation output must retain the exact accepted committed state")
		}
		delete(byPath, mount.Path)
	}
	return true, nil
}

func mergedPredeploymentContinuation(ctx context.Context, q store.Querier, task store.TaskRow, frozen protocol.IssueDeliveryCandidate, observed protocol.DeliveryObservation, next *protocol.IssueDeliveryCandidate) error {
	if observed.State != "merged" || observed.MergeSHA == "" || observed.LineageErrorKind != "" || !observed.TargetContainsMerge || observed.HeadSHA != frozen.HeadSHA || observed.BaseSHA != frozen.BaseSHA || next == nil || next.DeliveryID == frozen.DeliveryID || next.BaseSHA != observed.TargetSHA || next.TargetSHA != observed.TargetSHA || next.ProviderState != "open" {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original merge and current main before continuing its candidate")
	}
	if !next.Deploy || next.Deployment == nil || frozen.Deployment == nil || next.Deployment.ServerInstance != frozen.Deployment.ServerInstance || next.Deployment.ParkIdleManagers != frozen.Deployment.ParkIdleManagers || !sameDeploymentHosts(next.Deployment.HostIDs, frozen.Deployment.HostIDs) {
		return protocol.Errorf(protocol.CodeForbidden, "candidate continuation must retain the complete original deployment scope")
	}
	_, stage, err := taskPredeploymentResult(ctx, q, task)
	if err != nil {
		return err
	}
	if stage == nil || stage.DeliveryID != next.DeliveryID || stage.HeadSHA != next.HeadSHA || stage.BaseSHA != next.BaseSHA {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the new candidate requires its own matching predeployment artifact")
	}
	_, err = acceptedPredeploymentResult(ctx, q, task, stage)
	return err
}
