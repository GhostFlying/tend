package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func submitIssueOwnerResult(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload) (Outcome, error) {
	if p.Result == nil || p.Result.Report == nil || strings.TrimSpace(p.Result.Report.Body) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "submit_result requires the owner's actual readable result report")
	}
	runID := p.RunID
	if x.coordinationExecution != nil {
		actual := x.coordinationExecution.Selection.RunnerRunID
		if actual == "" || runID != "" && runID != actual {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "result must belong to the actual executing Issue owner Run")
		}
		runID = actual
	}
	run, err := store.GetRun(ctx, tx.Q(), runID)
	if err != nil {
		return Outcome{}, errNotFound(err, "actual owner Run required")
	}
	manager, err := store.GetIssueManagerTask(ctx, tx.Q(), run.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	if manager.IssueID != issue.IssueID || manager.AssignmentEpoch != issue.AssignmentEpoch || p.TaskID != "" && p.TaskID != run.TaskID {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "result belongs to another Issue responsibility")
	}
	allowed, err := managerNativeBusinessAllowed(ctx, tx.Q(), run)
	if err != nil {
		return Outcome{}, err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), runID)
	if err != nil {
		return Outcome{}, err
	}
	if !allowed || lease.Status != store.LeaseActive || lease.ExpiresAt <= x.Now.UnixMilli() {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "result requires the current active responsibility and lease")
	}
	// These mounts are result facts from the real producer, not a new workspace
	// or Task agreement. Preserve the same hash and upload validation as workers.
	workspace := domain.Workspace{Mode: domain.WorkspaceTemporary}
	for _, mount := range p.Result.Mounts {
		workspace.Mounts = append(workspace.Mounts, domain.Mount{Path: mount.Path, Repo: mount.Repo})
	}
	if err := checkResultDeliverables(ctx, tx.Q(), domain.Spec{Deliverables: []string{domain.DeliveryReport}, Workspace: workspace}, runID, *p.Result); err != nil {
		return Outcome{}, err
	}
	task, err := store.GetTask(ctx, tx.Q(), run.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	duty, err := store.GetIssueDeliveryDuty(ctx, tx.Q(), issue.IssueID, task.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		duty = protocol.IssueDeliveryDuty{TaskID: task.TaskID, Revision: 1, Status: "preparing", AssignmentEpoch: issue.AssignmentEpoch}
	} else if err != nil {
		return Outcome{}, err
	} else if duty.Status == "complete" || duty.Status == "report" || duty.Status == "ended" {
		if duty.ExecutionTaskID != "" {
			execution, err := store.GetTask(ctx, tx.Q(), duty.ExecutionTaskID)
			if err != nil {
				return Outcome{}, err
			}
			if execution.TaskID != task.TaskID {
				var busy bool
				if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r LEFT JOIN leases l USING(run_id) WHERE r.task_id=? AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR l.status<>'released')) OR EXISTS(SELECT 1 FROM workspaces WHERE occupied_by=?)`, execution.TaskID, execution.TaskID).Scan(&busy); err != nil {
					return Outcome{}, err
				}
				if busy {
					return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original delivery execution before replacing its settled responsibility")
				}
			}
			if err := archiveTaskDeliveryForContinuation(ctx, x, tx, execution); err != nil {
				return Outcome{}, err
			}
		}
		// The owner may continue within its still-active real Run. Preserve the
		// previous settlement and monotonically increasing duty revision, not its
		// ended candidate or authority as the new result's current responsibility.
		duty = protocol.IssueDeliveryDuty{TaskID: task.TaskID, Revision: duty.Revision + 1, Status: "preparing", AssignmentEpoch: issue.AssignmentEpoch}
	} else if duty.Candidate == nil {
		duty.Revision++
		duty.Status = "preparing"
	}
	if err := bindDeliveryUpgradeArtifact(ctx, tx, task, *p.Result); err != nil {
		return Outcome{}, err
	}
	raw, err := json.Marshal(p.Result)
	if err != nil {
		return Outcome{}, err
	}
	digest := sha256.Sum256(raw)
	artifact := x.IDs.New(protocol.PrefixArtifact)
	if err := tx.InsertArtifact(ctx, artifact, task.TaskID, runID, "result", string(raw), hex.EncodeToString(digest[:]), x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	policy, err := taskAcceptancePolicy(ctx, x, tx.Q(), issue, domain.Spec{}, p.IndependentAcceptance)
	if err != nil {
		return Outcome{}, err
	}
	policyRaw, err := json.Marshal(policy)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO issue_owner_results(artifact_id,issue_id,assignment_epoch,required_independent,policy_json,request_id,created_at) VALUES(?,?,?,?,?,?,?)`, artifact, issue.IssueID, issue.AssignmentEpoch, policy.Required, string(policyRaw), x.Env.RequestID, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET current_artifact_id=?,updated_at=? WHERE task_id=?`, artifact, x.Now.UnixMilli(), task.TaskID); err != nil {
		return Outcome{}, err
	}
	event := x.IDs.New(protocol.PrefixEvent)
	body, _ := json.Marshal(map[string]any{"issue_id": issue.IssueID, "task_id": task.TaskID, "run_id": runID, "artifact_id": artifact})
	actor, _ := json.Marshal(x.ActualPrincipal())
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "app", ObjectType: "artifact", ObjectID: artifact, Kind: "issue.result_submitted", BodyJSON: string(body), ActorJSON: string(actor), RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return Outcome{}, err
	}
	if duty.EventID == "" {
		duty.EventID = event
	}
	// The result is a new immutable fact from the same real producer. It does
	// not change or expire an already frozen candidate/execution authorization.
	duty.ArtifactID, duty.Reason = artifact, p.Reason
	dutyRaw, err := json.Marshal(duty)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,artifact_id,revision,status,body_json) VALUES(?,?,?,?,?,?,?) ON CONFLICT(issue_id,task_id) DO UPDATE SET event_id=excluded.event_id,artifact_id=excluded.artifact_id,revision=excluded.revision,status=excluded.status,body_json=excluded.body_json`, issue.IssueID, task.TaskID, duty.EventID, artifact, duty.Revision, duty.Status, string(dutyRaw)); err != nil {
		return Outcome{}, err
	}
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issueWithNextRevision(issue, x.Now.UnixMilli()), issue.Revision, canonicalOwnerIssueSource(p.IssueSourceReference), p, "issue.delivery_updated")
	out.Result = map[string]any{"artifact_id": artifact, "task_id": task.TaskID, "run_id": runID, "delivery": duty}
	return out, err
}

// An explicit independent requirement may be satisfied by a real reviewer Task
// inspecting this immutable owner artifact. Never copy a verdict from another
// result or claim the owner's continuing Task has finished.
func bindIssueOwnerResultAcceptance(ctx context.Context, _ *Exec, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload) error {
	task, err := store.GetTask(ctx, tx.Q(), p.TaskID)
	if err != nil {
		return err
	}
	var artifact, producer string
	err = tx.Q().QueryRowContext(ctx, `SELECT o.artifact_id,a.run_id FROM issue_owner_results o JOIN artifacts a USING(artifact_id) WHERE o.issue_id=? AND o.artifact_id=?`, issue.IssueID, task.CurrentArtifactID).Scan(&artifact, &producer)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Errorf(protocol.CodeNotFound, "the actual owner artifact is required")
		}
		return err
	}
	var reviewTask, reviewRun, reviewedArtifact, verdict, evidence string
	err = tx.Q().QueryRowContext(ctx, `SELECT a.task_id,a.run_id,a.artifact_id,a.verdict,a.evidence_json FROM acceptances a JOIN tasks t USING(task_id) WHERE a.acceptance_id=? AND a.verdict='passed' AND t.lifecycle='completed' AND a.spec_version=t.spec_version AND a.artifact_id=t.current_artifact_id`, p.AcceptanceID).Scan(&reviewTask, &reviewRun, &reviewedArtifact, &verdict, &evidence)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Errorf(protocol.CodeNotFound, "a real completed reviewer acceptance is required")
		}
		return err
	}
	var binding struct {
		ArtifactID string `json:"reviewed_artifact_id"`
	}
	if err := json.Unmarshal([]byte(evidence), &binding); err != nil || binding.ArtifactID != artifact {
		return protocol.Errorf(protocol.CodeInvalidRequest, "reviewer evidence must bind reviewed_artifact_id to this actual owner result")
	}
	primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), reviewTask)
	if err != nil {
		return err
	}
	var inScope bool
	if err := tx.Q().QueryRowContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT ? UNION ALL SELECT i.issue_id FROM issue_controls i JOIN tree ON i.parent_issue_id=tree.id) SELECT EXISTS(SELECT 1 FROM tree WHERE id=?)`, issue.IssueID, primary).Scan(&inScope); err != nil {
		return err
	}
	ownerRun, err := store.GetRunExecution(ctx, tx.Q(), producer)
	if err != nil {
		return err
	}
	reviewerRun, err := store.GetRunExecution(ctx, tx.Q(), reviewRun)
	if err != nil {
		return err
	}
	if !inScope || ownerRun == nil || reviewerRun == nil || ownerRun.Identity == reviewerRun.Identity {
		return protocol.Errorf(protocol.CodeForbidden, "the result needs an actual independent reviewer in this Issue responsibility")
	}
	result, err := tx.Exec(ctx, `UPDATE issue_owner_results SET acceptance_id=? WHERE artifact_id=? AND (acceptance_id IS NULL OR acceptance_id=?)`, p.AcceptanceID, artifact, p.AcceptanceID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the result already retains a different independent acceptance")
	}
	return nil
}

// An independent review is itself a result from a real Agent, not a request to
// independently review that review again. Bind its immutable target and verdict.
func bindIssueOwnerResultReview(ctx context.Context, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload) error {
	task, err := store.GetTask(ctx, tx.Q(), p.TaskID)
	if err != nil {
		return err
	}
	var original, producer string
	if err := tx.Q().QueryRowContext(ctx, `SELECT o.artifact_id,a.run_id FROM issue_owner_results o JOIN artifacts a USING(artifact_id) WHERE o.issue_id=? AND o.artifact_id=?`, issue.IssueID, task.CurrentArtifactID).Scan(&original, &producer); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Errorf(protocol.CodeNotFound, "the actual owner artifact is required")
		}
		return err
	}
	review, err := store.GetArtifact(ctx, tx.Q(), p.ReviewerArtifactID)
	if err != nil {
		return errNotFound(err, "the original independent review report is unavailable")
	}
	var report protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(review.RefJSON), &report); err != nil {
		return err
	}
	if report.Report == nil || report.Report.ReviewedArtifactID != original || report.Report.Verdict != protocol.VerdictPassed || strings.TrimSpace(report.Report.Body) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "the reviewer report must explicitly pass this exact reviewed_artifact_id")
	}
	policy, err := store.TaskAcceptancePolicy(ctx, tx.Q(), review.TaskID)
	if err != nil {
		return err
	}
	if ownerPolicy, err := store.OwnerResultPolicy(ctx, tx.Q(), review.ArtifactID); err != nil {
		return err
	} else if ownerPolicy != nil {
		policy = ownerPolicy
	}
	if policy == nil || policy.Required {
		// An explicit requirement on this review itself is extra work. Preserve
		// its real verdict; ordinary target reviews freeze Required=false.
		var accepted bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acceptances a JOIN artifacts original ON original.artifact_id=a.artifact_id JOIN runs r ON r.run_id=original.run_id JOIN plan_actions action ON action.action_id=r.action_id WHERE a.artifact_id=? AND a.verdict='passed' AND a.spec_version=action.target_spec_version)`, review.ArtifactID).Scan(&accepted); err != nil {
			return err
		}
		if !accepted {
			accepted, err = store.OwnerResultEvidence(ctx, tx.Q(), review.ArtifactID)
			if err != nil {
				return err
			}
		}
		if !accepted {
			return protocol.Errorf(protocol.CodeInvalidTransition, "this review itself explicitly requires its original independent acceptance")
		}
	}
	var inScope bool
	if err := tx.Q().QueryRowContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT ?1 UNION ALL SELECT i.issue_id FROM issue_controls i JOIN tree ON i.parent_issue_id=tree.id)
 SELECT (
 EXISTS(SELECT 1 FROM task_work_item_owners o JOIN tree ON tree.id=o.work_item_id WHERE o.task_id=?2)
 OR EXISTS(SELECT 1 FROM issue_manager_tasks m JOIN tree ON tree.id=m.issue_id WHERE m.task_id=?2)
 OR EXISTS(SELECT 1 FROM artifact_shares s JOIN artifacts a USING(artifact_id) WHERE s.issue_id=?1 AND s.artifact_id=?3 AND s.status='active' AND s.ref_json=a.ref_json)
 ) AND NOT EXISTS(SELECT 1 FROM artifact_shares s WHERE s.issue_id=?1 AND s.artifact_id=?3 AND s.status='revoked' AND NOT EXISTS(SELECT 1 FROM artifact_shares active WHERE active.issue_id=s.issue_id AND active.artifact_id=s.artifact_id AND active.status='active'))`, issue.IssueID, review.TaskID, review.ArtifactID).Scan(&inScope); err != nil {
		return err
	}
	ownerRun, err := store.GetRunExecution(ctx, tx.Q(), producer)
	if err != nil {
		return err
	}
	reviewerRun, err := store.GetRunExecution(ctx, tx.Q(), review.RunID)
	if err != nil {
		return err
	}
	if !inScope || ownerRun == nil || reviewerRun == nil || ownerRun.Identity == reviewerRun.Identity {
		return protocol.Errorf(protocol.CodeForbidden, "the original report needs a real independent reviewer in this Issue or an explicit result share")
	}
	result, err := tx.Exec(ctx, `UPDATE issue_owner_results SET reviewer_artifact_id=? WHERE artifact_id=? AND (reviewer_artifact_id IS NULL OR reviewer_artifact_id=?)`, review.ArtifactID, original, review.ArtifactID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the original result already retains a different independent review")
	}
	return nil
}
