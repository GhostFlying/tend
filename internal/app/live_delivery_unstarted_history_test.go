package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type deliveryRefreshFromCoordinator struct {
	UpdateIssueDelivery
	issue, run string
	epoch      int64
}

func (c deliveryRefreshFromCoordinator) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	actual := *x
	actual.coordinationExecution = &store.HostedExecution{IssueID: c.issue, AssignmentEpoch: c.epoch}
	actual.coordinationExecution.Selection.RunnerRunID = c.run
	return c.UpdateIssueDelivery.Apply(ctx, &actual, tx)
}

func TestLiveDeliveryRefreshFromCoordinatorPreservesTargetRun(t *testing.T) {
	h, _, issue, as, candidate := liveReviewedDeliveryFixture(t)
	prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
	original := bindSelfCandidateForTest(t, h, issue.IssueID, as.TaskID)
	var caller string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT run_id FROM runs WHERE task_id<>? ORDER BY created_at,run_id LIMIT 1`, as.TaskID).Scan(&caller); err != nil {
			return err
		}
		run, err := store.GetRun(t.Context(), tx.Q(), caller)
		if err != nil {
			return err
		}
		primary, err := store.GetTaskIssueOwner(t.Context(), tx.Q(), run.TaskID)
		if err == nil && primary != issue.IssueID {
			t.Fatal("fixture coordinator belongs to a different Issue", primary)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	candidate = observeSelfRefreshTarget(t, h, issue.IssueID, candidate)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Refresh the target Task without attributing its history to the coordinator"}}
	op := h.exec(owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), deliveryRefreshFromCoordinator{issue: issue.IssueID, run: caller, epoch: issue.AssignmentEpoch})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("current owner cannot refresh a related Task", op)
	}
	read, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: as.TaskID, DutyRevision: original.Revision})
	if err != nil || read.DeliveryAuthorization == nil || read.DeliveryAuthorization.RunID != as.RunID || read.DeliveryAuthorization.RunID == caller || read.DeliveryAuthorization.Valid || read.DeliveryAuthorization.Settlement.Eligible {
		t.Fatal("history lost or fabricated its real target execution", read, err)
	}
}

func TestLiveDeliveryRefreshBeforeFirstRunKeepsAbsentProvenance(t *testing.T) {
	h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, source, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	raw, err := json.Marshal(h.storedSpec(source, 1))
	if err != nil {
		t.Fatal(err)
	}
	var spec protocol.OwnerTaskSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{Title: "Continuous delivery before execution", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: candidate.DeliveryID, TaskID: created.TaskID}))
	prepareLiveDeliveryForTest(t, h, issue.IssueID, created.TaskID, candidate)
	original := bindSelfCandidateForTest(t, h, issue.IssueID, created.TaskID)
	tasks, runs, artifacts := h.count("tasks"), h.count("runs"), h.count("artifacts")
	candidate = observeSelfRefreshTarget(t, h, issue.IssueID, candidate)
	op = updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: created.TaskID, ExpectedTaskControlRevision: rev(h.task(created.TaskID).ControlRevision), Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Keep the actual unstarted binding as history, without inventing an execution"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: created.TaskID})
	if err != nil || current.DeliveryAuthorization == nil || len(current.DeliveryAuthorization.History) != 1 {
		t.Fatal("new session cannot discover the unstarted candidate history", current, err)
	}
	ref := current.DeliveryAuthorization.History[0]
	if ref.RunID != "" || ref.ExecutionRunID != "" || ref.Generation != 0 || ref.DutyRevision != original.Revision {
		t.Fatal("history fabricated an execution", ref)
	}
	history, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: created.TaskID, DutyRevision: ref.DutyRevision})
	if err != nil || history.DeliveryAuthorization == nil {
		t.Fatal("Task/revision cannot read history before the first Run", history, err)
	}
	v := history.DeliveryAuthorization
	if !v.Historical || v.Valid || v.RunID != "" || v.DutyStatus != "executing" || v.Settlement == nil || v.Settlement.Eligible || v.Settlement.ArtifactID != "" || v.Candidate.TargetSHA != original.Candidate.TargetSHA {
		t.Fatal("superseded candidate became a fictional completed result", v)
	}
	bindSelfCandidateForTest(t, h, issue.IssueID, created.TaskID)
	if h.count("tasks") != tasks || h.count("runs") != runs || h.count("artifacts") != artifacts {
		t.Fatal("candidate replacement created phase Tasks, Runs or artifacts")
	}
}
