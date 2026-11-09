package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// agentTechnicalWait is deliberately evidence-based: a human answer relayed by
// an Agent, or an old answer without its actual actor/source, stays human-owned.
func agentTechnicalWait(ctx context.Context, q store.Querier, d store.DecisionRow) (bool, error) {
	if d.Status != store.DecisionAnswered || strings.TrimSpace(d.AnswerJSON) == "" {
		return false, nil
	}
	var answer decisionAnswer
	if err := json.Unmarshal([]byte(d.AnswerJSON), &answer); err != nil {
		return false, err
	}
	if answer.Actor == nil || answer.Continuation == nil || answer.Continuation.Mode != protocol.DecisionWait || d.AnsweredMessageID == "" {
		return false, nil
	}
	actor := answer.Actor
	if actor.Kind != protocol.PrincipalMainAgent && actor.Kind != protocol.PrincipalBackgroundAgent && (actor.Kind != protocol.PrincipalRun || actor.Role != protocol.RoleIssueManager) {
		return false, nil
	}
	var agent bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND author='agent')`, d.AnsweredMessageID).Scan(&agent)
	return agent, err
}

func agentTechnicalWaitScope(ctx context.Context, q store.Querier, e store.HostedExecution, d store.DecisionRow) (bool, error) {
	technical, err := agentTechnicalWait(ctx, q, d)
	if err != nil || !technical {
		return false, err
	}
	primary, err := store.GetTaskIssueOwner(ctx, q, d.TaskID)
	if err != nil {
		return false, err
	}
	if primary == "" {
		return false, nil
	}
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if err != nil {
		return false, err
	}
	if issue.AssignmentState != "active" || (issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused) {
		return false, nil
	}
	if e.Selection.Purpose == agentconfig.Main {
		return true, nil
	}
	return e.Selection.Purpose == agentconfig.IssueManager && e.IssueID == primary && e.AssignmentEpoch == issue.AssignmentEpoch && issue.Assignee.Ref == e.Selection.Identity.ExecutionEnvironmentID+"/"+e.Selection.Identity.LocalID, nil
}

func admitStoppedResponsibilityAdoption(ctx context.Context, x *Exec, tx *store.Tx, v protocol.DecisionContinuationView, p protocol.ResumeDecisionContinuationPayload) error {
	if p.ExpectedAssignmentEpoch == nil || strings.TrimSpace(p.Reason) == "" {
		return protocol.Errorf(protocol.CodeStaleRevision, "original handoff responsibility changed; explicitly identify the current assignment epoch and continuation reason")
	}
	contract, err := store.GetTaskOwnerContract(ctx, tx.Q(), v.TaskID)
	if err != nil {
		return errNotFound(err, "stopped responsibility requires its original named execution contract")
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), contract.IssueID)
	if err != nil {
		return err
	}
	if issue.AssignmentEpoch != *p.ExpectedAssignmentEpoch || issue.AssignmentState != "active" || issue.State != protocol.IssueActive || issue.Assignee.Kind != protocol.IssueAssigneeAgent || issue.Assignee.Ref != contract.AgentRef {
		return protocol.Errorf(protocol.CodeStaleRevision, "current execution responsibility changed")
	}
	agreement, err := store.QuestionHandoffAgreementCurrent(ctx, tx.Q(), v.RunID)
	if err != nil {
		return err
	}
	handoff, err := store.GetQuestionHandoff(ctx, tx.Q(), v.RunID)
	if err != nil {
		return err
	}
	run, err := store.GetRun(ctx, tx.Q(), v.RunID)
	if err != nil {
		return err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), v.RunID)
	if err != nil {
		return err
	}
	if !agreement || handoff == nil || !handoff.Stopped || handoff.DecisionID != v.DecisionID || run.ObservedState != protocol.RunFinished || run.Role != protocol.RoleImplement || lease.Status != store.LeaseReleased {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "original execution must be a proved stopped implementation handoff")
	}
	var active bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed'))`, v.TaskID).Scan(&active); err != nil {
		return err
	}
	if active {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "another original Task execution still requires reconciliation")
	}
	original, err := store.GetRunExecution(ctx, tx.Q(), v.RunID)
	if err != nil {
		return err
	}
	manager, err := store.CurrentIssueManagerTask(ctx, tx.Q(), issue.IssueID)
	if err != nil {
		return errNotFound(err, "current responsible Agent has no accepted execution pin")
	}
	if manager.State != "assigned" || manager.AssignmentEpoch != issue.AssignmentEpoch || manager.Pin == nil {
		return protocol.Errorf(protocol.CodeStaleRevision, "current manager responsibility changed")
	}
	current, err := store.WorkerPinForManager(ctx, tx.Q(), *manager.Pin)
	if err != nil {
		return err
	}
	if contract.Execution == nil || original == nil || *contract.Execution != *original || *original != current || manager.Pin.Profile != original.Profile || manager.Pin.Runtime != original.Runtime {
		return protocol.Errorf(protocol.CodeStaleRevision, "stopped continuation requires the same complete named execution pin")
	}
	task, err := store.GetTask(ctx, tx.Q(), v.TaskID)
	if err != nil {
		return err
	}
	return tx.InsertResponsibilityAdoption(ctx, store.ResponsibilityAdoption{RequestDomain: x.Principal.DedupDomain(), RequestID: x.Env.RequestID, TaskID: v.TaskID, SpecVersion: task.SpecVersion, TaskControlRevision: task.ControlRevision, DecisionID: v.DecisionID, ContinuationRevision: v.Revision, SourceRunID: v.RunID, IssueID: issue.IssueID, FromAssignmentEpoch: contract.AssignmentEpoch, AssignmentEpoch: issue.AssignmentEpoch, AgentRef: contract.AgentRef, Execution: *original, Reason: strings.TrimSpace(p.Reason), CreatedAt: x.Now.UnixMilli()})
}
