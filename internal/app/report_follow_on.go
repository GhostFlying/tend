package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const (
	reportFollowOnWaitKey = "report-follow-on"
	reportWaitSuperseded  = "report_superseded"
)

func reportRequestedAcceptance(p protocol.UpdateIssueDeliveryPayload) *bool {
	if p.IndependentAcceptance == nil {
		return nil
	}
	return &p.IndependentAcceptance.Required
}

func reportAttentionID(issue, artifact string) string {
	return protocol.NativeObjectID(protocol.Prefix("attn"), issue, "report-follow-on/"+artifact)
}

func latestReportAttention(ctx context.Context, q store.Querier, issue string) (protocol.Attention, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT attention_id FROM attentions
 WHERE issue_id=? AND json_type(body_json,'$.report_follow_on')='object'
 ORDER BY rowid DESC LIMIT 1`, issue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Attention{}, store.ErrNotFound
	}
	if err != nil {
		return protocol.Attention{}, err
	}
	return store.GetAttention(ctx, q, id)
}

// Report settlement creates the question and parks the original owner in the
// same transaction. A stale transport retry can recover all three facts.
func settleReportFollowOn(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, duty protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) ([]Event, bool, error) {
	if !issue.Plan.ReportFollowOnDecision {
		return nil, false, protocol.Errorf(protocol.CodeInvalidRequest, "the Issue is not configured to ask about implementation")
	}
	task, err := store.GetTask(ctx, tx.Q(), duty.TaskID)
	if err != nil {
		return nil, false, err
	}
	id := reportAttentionID(issue.IssueID, task.CurrentArtifactID)
	scope := strings.TrimSpace(p.ProposedImplementationScope)
	if a, err := store.GetAttention(ctx, tx.Q(), id); err == nil {
		if a.ReportFollowOn == nil {
			return nil, false, protocol.Errorf(protocol.CodeConflictPayload, "report decision identity is already used")
		}
		if a.ReportFollowOn.ArtifactID == task.CurrentArtifactID && scope == a.ReportFollowOn.ImplementationScope {
			return nil, true, nil
		}
		return nil, false, protocol.Errorf(protocol.CodeConflictPayload, "the original report already has its own implementation decision")
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	if previous, err := latestReportAttention(ctx, tx.Q(), issue.IssueID); err == nil {
		if previous.Status == "open" {
			return nil, false, protocol.Errorf(protocol.CodeInvalidTransition, "the previous report is still awaiting its original decision")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	if scope == "" || len(scope) > 3000 {
		return nil, false, protocol.Errorf(protocol.CodeInvalidRequest, "report settlement requires a concrete proposed_implementation_scope of up to 3000 bytes")
	}
	if issue.State != protocol.IssueActive || issue.BoardStatus() == protocol.IssueBacklog {
		return nil, false, protocol.Errorf(protocol.CodeInvalidTransition, "report decision requires a currently admitted Issue")
	}
	artifact, err := store.GetArtifact(ctx, tx.Q(), task.CurrentArtifactID)
	if err != nil {
		return nil, false, err
	}
	var result protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(artifact.RefJSON), &result); err != nil {
		return nil, false, err
	}
	if result.Report == nil || strings.TrimSpace(result.Report.Body) == "" {
		return nil, false, protocol.Errorf(protocol.CodeInvalidTransition, "the decision requires a readable completed report")
	}
	runID := p.RunID
	if x.coordinationExecution != nil {
		actual := x.coordinationExecution.Selection.RunnerRunID
		if runID != "" && runID != actual {
			return nil, false, protocol.Errorf(protocol.CodeForbidden, "use the actual Issue owner Run")
		}
		runID = actual
	}
	run, err := store.GetRun(ctx, tx.Q(), runID)
	if err != nil {
		return nil, false, errNotFound(err, "the actual Issue owner Run is required")
	}
	manager, err := store.GetIssueManagerTask(ctx, tx.Q(), run.TaskID)
	if err != nil {
		return nil, false, errNotFound(err, "the actual Issue owner Run is required")
	}
	if run.Role != protocol.RoleIssueManager || manager.IssueID != issue.IssueID || manager.AssignmentEpoch != issue.AssignmentEpoch {
		return nil, false, protocol.Errorf(protocol.CodeForbidden, "the Run must belong to this Issue's current owner")
	}
	allowed, err := managerNativeBusinessAllowed(ctx, tx.Q(), run)
	if err != nil {
		return nil, false, err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), runID)
	if err != nil {
		return nil, false, err
	}
	if !allowed || run.Protocol < 3 || lease.Status != store.LeaseActive || lease.ExpiresAt <= x.Now.UnixMilli() {
		return nil, false, protocol.Errorf(protocol.CodeInvalidTransition, "report decision requires a live Issue owner Run with external wait support")
	}

	inner, env := *x, *x.Env
	inner.Env = &env
	env.Target = protocol.Target{Type: "attention", ID: id}
	env.ExpectedControlRevision = nil
	env.Payload, _ = json.Marshal(protocol.CreateAttentionPayload{
		IssueID: issue.IssueID, Kind: "decision", BlocksClose: true,
		Title:   "报告已交付。是否继续以下实施范围？\n\n" + scope,
		Options: []string{protocol.ReportContinueImplementation, protocol.ReportMoveToBacklog, protocol.ReportCloseOnly},
	})
	attention, err := createAttentionWithReport(ctx, &inner, tx, &protocol.ReportFollowOn{
		ArtifactID: artifact.ArtifactID, RunID: runID, AssignmentEpoch: issue.AssignmentEpoch, ImplementationScope: scope,
	})
	if err != nil {
		return nil, false, err
	}
	a := attention.Result.(protocol.Attention)
	env.Target = protocol.Target{Type: "run", ID: runID}
	env.Payload, _ = json.Marshal(protocol.WaitExternalPayload{
		WaitKey: reportFollowOnWaitKey, Kind: "answer", TargetID: a.DecisionID, IssueID: issue.IssueID,
		Reason:   "Wait for the user's implementation decision after report delivery.",
		Recovery: "Read the original Attention and its authenticated answer. Implement only the scope approved by 继续实现. For 仅报告结案, reconcile any remaining closure obligations and use the original report and decision as evidence.",
	})
	wait, err := (WaitExternal{}).Apply(ctx, &inner, tx)
	if err != nil {
		return nil, false, err
	}
	return append(attention.Events, wait.Events...), false, nil
}

func validateReportFollowOnAnswer(ctx context.Context, x *Exec, q store.Querier, a protocol.Attention, p protocol.AnswerDecisionPayload, supervisor bool) error {
	if supervisor {
		return protocol.Errorf(protocol.CodeForbidden, "implementation decisions require an authenticated owner answer")
	}
	if p.Option == "" || strings.TrimSpace(p.Text) != "" || p.Continuation != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "choose one of the original report options; discuss a different implementation scope in the Issue")
	}
	issue, err := store.GetCanonicalIssue(ctx, q, a.IssueID)
	if err != nil {
		return err
	}
	if !reportDecisionCurrent(issue, a) {
		return protocol.Errorf(protocol.CodeStaleRevision, "the report decision belongs to a superseded Issue responsibility")
	}
	return validateAttentionOwnerRelay(ctx, q, x, a.IssueID, p.SourceMessageID)
}

// The answer and physical-stop transactions can arrive in either order.
// Only the one that observes both may apply a report-only endpoint.
func applyReportFollowOnAnswer(ctx context.Context, x *Exec, tx *store.Tx, a protocol.Attention, d store.DecisionRow, option string) ([]Event, error) {
	v, err := store.GetExternalWait(ctx, tx.Q(), a.ReportFollowOn.RunID)
	if err != nil || v == nil || v.WaitKey != reportFollowOnWaitKey || v.TargetID != d.DecisionID || v.Status != "waiting" || !v.Stopped || v.WakeReason == reportWaitSuperseded {
		return nil, err
	}
	run, err := store.GetRun(ctx, tx.Q(), v.RunID)
	if err != nil {
		return nil, err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), v.RunID)
	if err != nil || run.ObservedState != protocol.RunFinished || lease.Status != store.LeaseReleased {
		return nil, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), a.IssueID)
	if err != nil {
		return nil, err
	}
	if !reportDecisionCurrent(issue, a) {
		return nil, nil
	}
	if option == protocol.ReportContinueImplementation {
		// The ordinary external-wait observer queues the same owner Task, using
		// the original action and actual stopped receipt.
		return nil, tx.PutExternalWait(ctx, *v, x.Now.UnixMilli())
	}
	if option != protocol.ReportMoveToBacklog && option != protocol.ReportCloseOnly {
		return nil, nil
	}
	if a.Status == "open" {
		a.Status = "resolved"
		if err := saveAttention(ctx, x, tx, &a, a.Revision); err != nil {
			return nil, err
		}
	}
	if err := tx.Savepoint(ctx, "report_follow_on_endpoint"); err != nil {
		return nil, err
	}
	inner, env := *x, *x.Env
	inner.Env = &env
	inner.Actor, inner.Principal = nil, protocol.Principal{Kind: protocol.PrincipalSystem}
	inner.coordinationExecution = nil
	inner.coordinationID, inner.coordinationEffectSeq = "", 0
	env.Target = protocol.Target{Type: "issue", ID: issue.IssueID}
	reason := "Owner chose " + option + " for the delivered report"
	var out Outcome
	if option == protocol.ReportMoveToBacklog {
		env.Operation = "issues.set_status"
		env.Payload, _ = json.Marshal(protocol.SetIssueStatusPayload{Status: protocol.IssueBacklog, ExpectedAssignmentEpoch: issue.AssignmentEpoch})
		out, err = setCanonicalIssueStatus(ctx, &inner, tx, issue)
	} else {
		env.Operation = "issues.close"
		inner.reportDecisionEndpoint = true
		out, err = closeReportOnly(ctx, &inner, tx, issue, a, reason)
	}
	if err != nil {
		if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
			return nil, err
		}
		if rollback := tx.RollbackTo(ctx, "report_follow_on_endpoint"); rollback != nil {
			return nil, rollback
		}
		// Keep the actual answer. A separate outstanding obligation still
		// belongs to the original manager and must be settled before closure.
		v.ResumeReason = err.Error()
		return []Event{attentionEvent(a)}, tx.PutExternalWait(ctx, *v, x.Now.UnixMilli())
	}
	if err := tx.Release(ctx, "report_follow_on_endpoint"); err != nil {
		return nil, err
	}
	v.Status, v.WakeReason = "cancelled", "report_endpoint"
	if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
		return nil, err
	}
	if err := tx.SetActionStatus(ctx, run.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
		return nil, err
	}
	return append([]Event{attentionEvent(a)}, out.Events...), nil
}

func closeReportOnly(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, a protocol.Attention, reason string) (Outcome, error) {
	artifact, err := store.GetArtifact(ctx, tx.Q(), a.ReportFollowOn.ArtifactID)
	if err != nil {
		return Outcome{}, err
	}
	duty, err := store.GetIssueDeliveryDuty(ctx, tx.Q(), issue.IssueID, artifact.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	if duty.Status != "report" || len(duty.Evidence) != 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the original report delivery requires reconciliation")
	}
	evidence := append([]protocol.IssueEvidence{}, duty.Evidence...)
	evidence = append(evidence, protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: a.AttentionID, Summary: reason})
	x.Env.Payload, _ = json.Marshal(canonicalOwnerEndpoint{
		canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: reason},
		Closure:               &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: reason, Evidence: evidence},
	})
	return applyCanonicalOwnerLifecycle(ctx, x, tx, "close", issue)
}

func settleStoppedReportFollowOn(ctx context.Context, x *Exec, tx *store.Tx, v *protocol.ExternalWait) ([]Event, error) {
	d, err := store.GetDecision(ctx, tx.Q(), v.TargetID)
	if err != nil || d.Status != store.DecisionAnswered {
		return nil, err
	}
	a, found, err := attentionForDecision(ctx, tx.Q(), d.DecisionID)
	if err != nil || !found || a.ReportFollowOn == nil {
		return nil, err
	}
	var answer decisionAnswer
	if err := json.Unmarshal([]byte(d.AnswerJSON), &answer); err != nil {
		return nil, err
	}
	return applyReportFollowOnAnswer(ctx, x, tx, a, d, answer.Option)
}

func reportFollowOnClosureGuard(ctx context.Context, q store.Querier, issue protocol.Issue) error {
	a, err := latestReportAttention(ctx, q, issue.IssueID)
	if errors.Is(err, store.ErrNotFound) || err == nil && a.ReportFollowOn == nil {
		return protocol.Errorf(protocol.CodeInvalidTransition, "settle the formal report and its owner decision before completing the Issue")
	}
	if err != nil {
		return err
	}
	if a.Status == "open" {
		return nil // The ordinary Attention blocker supplies the original identity.
	}
	if a.Status != "resolved" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "settle the renewed report decision before completing the Issue")
	}
	v, err := store.GetExternalWait(ctx, q, a.ReportFollowOn.RunID)
	if err != nil {
		return err
	}
	if v == nil || !v.Stopped {
		return protocol.Errorf(protocol.CodeInvalidTransition, "wait for the original report execution to confirm physical stop")
	}
	return nil
}

func reportDecisionCurrent(issue protocol.Issue, a protocol.Attention) bool {
	return a.ReportFollowOn != nil && a.Status != "withdrawn" && issue.AssignmentEpoch == a.ReportFollowOn.AssignmentEpoch &&
		issue.Plan.ReportFollowOnDecision && issue.AssignmentState == "active" &&
		(issue.State == protocol.IssueActive || issue.State == protocol.IssuePaused) &&
		issue.BoardStatus() != protocol.IssueBacklog
}

// Changes to admission or scope retire the open question without answering it.
// The stopped Run and its published report remain durable.
func retireObsoleteReportDecision(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue) ([]Event, error) {
	a, err := latestReportAttention(ctx, tx.Q(), issue.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil || a.ReportFollowOn == nil || reportDecisionCurrent(issue, a) {
		return nil, err
	}
	v, err := store.GetExternalWait(ctx, tx.Q(), a.ReportFollowOn.RunID)
	if err != nil {
		return nil, err
	}
	if v != nil && (v.Status == "stopping" || v.Status == "waiting") && v.WakeReason != reportWaitSuperseded {
		v.WakeReason = reportWaitSuperseded
		if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
			return nil, err
		}
	}
	if a.Status != "open" {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE decisions SET status='cancelled',version=version+1,updated_at=? WHERE decision_id=? AND status='open'`, x.Now.UnixMilli(), a.DecisionID); err != nil {
		return nil, err
	}
	a.Status, a.Resolution = "withdrawn", "The Issue's scope, admission or responsibility changed"
	if err := saveAttention(ctx, x, tx, &a, a.Revision); err != nil {
		return nil, err
	}
	return []Event{attentionEvent(a)}, nil
}
