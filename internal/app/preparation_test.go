package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func preparationAssignment(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment) {
	t.Helper()
	h, runtime, issue := issueOwnerHarness(t)
	created := createOwnerExecution(t, h, issue, "implementer")
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	original := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	report := protocol.WorkspaceReport{BackendID: original.Workspace.BackendID, Mounts: []protocol.ResolvedMount{{Path: "tend", ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: domain.TaskBranch(original.Workspace.Name)}}}
	if op := h.runAs(original, protocol.OpRunsReportWorkspace, report, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(original, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(original.TaskID), map[string]any{}, rev(h.task(original.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if !h.releaseResource(original.TaskID, original.Workspace.ID) {
		t.Fatal("original workspace not released")
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: original.TaskID}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Continue retained work", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if !as.PreparationDiagnostics || !as.Workspace.Reused {
		t.Fatal("diagnosis not negotiated for reuse", as)
	}
	return h, runtime, canonicalTaskIssue(t, h, issue.IssueID), as
}

func preparationPayload(as protocol.Assignment) protocol.ReleasePayload {
	var spec domain.Spec
	_ = json.Unmarshal(as.Spec, &spec)
	return protocol.ReleasePayload{Reason: "workspace_prepare_failed", Preparation: &protocol.RunPreparationFailure{
		Phase: protocol.PreparationPhaseWorkspace, Code: protocol.PreparationInheritedChanges, WorkspaceID: as.Workspace.ID,
		SourceTaskID: spec.Workspace.SourceTaskID, RequiredField: protocol.PreparationIncludeField, ChangeCount: 1,
		Changes: []protocol.PreparationChange{{Mount: "tend", Path: "retained.txt", Status: " M", HEAD: strings.Repeat("a", 40)}},
	}}
}

func TestFault_PreparationFailureWakesOwnerAndCorrectsSameTask(t *testing.T) {
	h, runtime, issue, as := preparationAssignment(t)
	payload := preparationPayload(as)
	env := h.env(protocol.OpRunsRelease, runTarget(as.RunID), payload, nil)
	op := h.exec(runPrincipal(as.RunID, as.Role), env, Release{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(as.TaskID).BlockReason != protocol.BlockPreparationRequired || len(h.openDecisions(as.TaskID)) != 0 {
		t.Fatal("technical defect became a human retry decision", h.task(as.TaskID))
	}
	sources, outbox := h.count("background_sources"), h.count("outbox")
	if replay := h.exec(runPrincipal(as.RunID, as.Role), env, Release{}); string(replay.Result) != string(op.Result) || h.count("background_sources") != sources || h.count("outbox") != outbox {
		t.Fatal("release replay duplicated wake", replay)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.kind='task.preparation_blocked' AND b.work_item_id=?`, issue.IssueID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("owner wake not durable: %d", count)
		}
		detail, err := views.Task(t.Context(), tx.Q(), as.TaskID)
		if err != nil {
			return err
		}
		if len(detail.Runs) != 1 || !reflect.DeepEqual(detail.Runs[0].Preparation, payload.Preparation) {
			t.Fatal("task view lost exact failure evidence", detail.Runs)
		}
		if detail.Execution == nil || detail.Execution.State != protocol.ExecutionQueued || detail.Execution.QueueReason == nil || detail.Execution.QueueReason.Code != protocol.QueueTaskBlocked {
			t.Fatal("contract repair rendered as user wait or idle", detail.Execution)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Resource-health sweeps must not clear a contract defect or dispatch it.
	h.checkWorkspaces()
	if h.task(as.TaskID).BlockReason != protocol.BlockPreparationRequired {
		t.Fatal("host health cleared contract blocker")
	}
	spec := h.storedSpec(as.TaskID, as.SpecVersion)
	update := func() protocol.Operation {
		var input protocol.OwnerTaskSpec
		body, _ := json.Marshal(spec)
		if err := json.Unmarshal(body, &input); err != nil {
			t.Fatal(err)
		}
		return h.exec(owner, h.env("issues.update_task", taskTarget(as.TaskID), protocol.UpdateIssueTaskPayload{Spec: &input, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(h.task(as.TaskID).ControlRevision)), CanonicalOwnerTaskUpdate{})
	}
	if result := update(); result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	if h.task(as.TaskID).SpecVersion != 1 || h.task(as.TaskID).BlockReason != protocol.BlockPreparationRequired {
		t.Fatal("unchanged spec retried preparation")
	}
	tasks := h.count("tasks")
	spec.Workspace.IncludeExistingChanges = true
	if result := update(); result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	if h.count("tasks") != tasks || h.task(as.TaskID).SpecVersion != 2 || h.task(as.TaskID).BlockReason != protocol.BlockNone {
		t.Fatal("correction did not reuse task", h.task(as.TaskID))
	}
	next := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if next.TaskID != as.TaskID || next.SpecVersion != 2 || next.RunID == as.RunID || next.Workspace.ID != as.Workspace.ID {
		t.Fatal("correction changed resource or replayed old execution", next)
	}
	if h.run(as.RunID).ObservedState != protocol.RunFailed {
		t.Fatal("failed execution history rewritten")
	}
}

func TestFault_PreparationEvidenceRejectsMismatchesAndFencesStartedRuns(t *testing.T) {
	h, _, _, as := preparationAssignment(t)
	for _, field := range []string{"workspace", "source", "phase", "code", "started", "field", "path", "count", "head", "status", "mount"} {
		t.Run(field, func(t *testing.T) {
			p := preparationPayload(as)
			switch field {
			case "workspace":
				p.Preparation.WorkspaceID = "another"
			case "source":
				p.Preparation.SourceTaskID = "another"
			case "phase":
				p.Preparation.Phase = "execution"
			case "code":
				p.Preparation.Code = "unknown"
			case "started":
				p.Preparation.WorkerStarted = true
			case "field":
				p.Preparation.RequiredField = "other"
			case "path":
				p.Preparation.Changes[0].Path = "../outside"
			case "count":
				p.Preparation.ChangeCount = 0
			case "head":
				p.Preparation.Changes[0].HEAD = "untrusted"
			case "status":
				p.Preparation.Changes[0].Status = "xx"
			case "mount":
				p.Preparation.Changes[0].Mount = "other"
			}
			op := h.runAs(as, protocol.OpRunsRelease, p, Release{})
			if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest || h.run(as.RunID).ObservedState != protocol.RunStarting || h.count("run_preparation_failures") != 0 {
				t.Fatal("mismatched evidence changed execution", op)
			}
		})
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpRunsRelease, preparationPayload(as), Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal("final fact could not settle started run", op)
	}
	if len(h.openDecisions(as.TaskID)) != 1 || h.task(as.TaskID).BlockReason != protocol.BlockWaitingDecision {
		t.Fatal("started run entered automatic contract repair")
	}
	// An ordinary failure, with no new evidence, keeps the legacy behavior.
	h, _, _, as = preparationAssignment(t)
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "workspace_prepare_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if len(h.openDecisions(as.TaskID)) != 1 || h.task(as.TaskID).BlockReason != protocol.BlockWaitingDecision {
		t.Fatal("legacy failure behavior changed")
	}
}

func TestFault_PreparationReleaseAfterSpecCorrectionRetainsFrozenFacts(t *testing.T) {
	h, runtime, issue, as := preparationAssignment(t)
	spec := h.storedSpec(as.TaskID, as.SpecVersion)
	spec.Workspace.IncludeExistingChanges = true
	var input protocol.OwnerTaskSpec
	body, _ := json.Marshal(spec)
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("issues.update_task", taskTarget(as.TaskID), protocol.UpdateIssueTaskPayload{Spec: &input, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(h.task(as.TaskID).ControlRevision)), CanonicalOwnerTaskUpdate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpRunsRelease, preparationPayload(as), Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal("old frozen failure could not settle", op)
	}
	if h.task(as.TaskID).SpecVersion != 2 || h.task(as.TaskID).BlockReason != protocol.BlockNone || len(h.openDecisions(as.TaskID)) != 0 || h.run(as.RunID).ObservedState != protocol.RunFailed || h.count("run_preparation_failures") != 1 {
		t.Fatal("historical failure changed corrected contract", h.task(as.TaskID))
	}
	next := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if next.SpecVersion != 2 || next.TaskID != as.TaskID || next.Workspace.ID != as.Workspace.ID {
		t.Fatal("late old release blocked corrected execution", next)
	}
}

func TestFault_PreparationJournalReleaseSettlesAfterLeaseReconciliation(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "reconciled running"}[reconcile], func(t *testing.T) {
			h, runtime, _, as := preparationAssignment(t)
			h.clock.Advance(DefaultLeaseTTL + time.Second)
			if _, err := h.app.ExpireLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if reconcile {
				runtime.Sequence++
				p := protocol.HelloPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: runtime, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}
				if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), Hello{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if op := h.runAs(as, protocol.OpRunsRelease, preparationPayload(as), Release{}); op.Status != protocol.OpSucceeded {
				t.Fatal("late final fact stranded native capacity", op)
			}
			if h.run(as.RunID).ObservedState != protocol.RunFailed || h.count("run_preparation_failures") != 1 || h.task(as.TaskID).BlockReason == protocol.BlockPreparationRequired {
				t.Fatal("uncertain projection entered automatic repair or lost final fact")
			}
		})
	}
}

func TestFault_PreparationFailureRetainsFactsAfterHandoff(t *testing.T) {
	h, _, issue, as := preparationAssignment(t)
	op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Human takes over"}, rev(issue.Revision)), HostedIssueAssignment{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpRunsRelease, preparationPayload(as), Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("run_preparation_failures") != 1 || h.task(as.TaskID).ControlMode != protocol.ControlPaused || len(h.openDecisions(as.TaskID)) != 0 || h.task(as.TaskID).BlockReason == protocol.BlockPreparationRequired {
		t.Fatal("old owner failure advanced current responsibility")
	}
}

func TestRunnerIssueOwnerReadsOnlyItsAdmittedRunMessages(t *testing.T) {
	h, runtime, issue, as := preparationAssignment(t)
	if op := h.runAs(as, protocol.OpRunsRelease, preparationPayload(as), Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	manager := ownerClaim(t, h, runtime)
	if manager.Role != protocol.RoleIssueManager {
		t.Fatal("failure did not schedule owner", manager)
	}
	turn, _ := startHostedIssueReview(t, h, issue)
	p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: manager.RunID, Role: protocol.RoleIssueManager, ConversationID: "issue:" + issue.IssueID, Thread: turn.ThreadID}
	if _, err := h.app.RunMessages(t.Context(), p, as.RunID, 0, 10, false); err != nil {
		t.Fatal("current owner cannot diagnose its run", err)
	}
	for _, field := range []string{"run", "thread", "conversation"} {
		wrong := p
		switch field {
		case "run":
			wrong.Subject = as.RunID
		case "thread":
			wrong.Thread = "wrong"
		case "conversation":
			wrong.ConversationID = "issue:other"
		}
		if _, err := h.app.RunMessages(t.Context(), wrong, as.RunID, 0, 10, false); err == nil {
			t.Fatal("caller escaped admitted coordination", field)
		}
	}
	h.createTask(owner, createTaskPayload{Title: "Unrelated task", Spec: testSpec})
	unrelated, ok, _ := h.claim("linux-1")
	if !ok || unrelated.Role != protocol.RoleImplement {
		t.Fatal("unrelated run not scheduled", unrelated)
	}
	if _, err := h.app.RunMessages(t.Context(), p, unrelated.RunID, 0, 10, false); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("owner escaped primary Issue", err)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Fence old reader"}, rev(issue.Revision)), HostedIssueAssignment{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.RunMessages(t.Context(), p, as.RunID, 0, 10, false); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("handoff left old owner read admission", err)
	}
}

func TestFault_PreparationFailurePreservesHumanControl(t *testing.T) {
	for _, mode := range []string{"pause", "takeover", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _, as := preparationAssignment(t)
			var op protocol.Operation
			if mode == "cancel" {
				op = h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), map[string]any{}, rev(h.task(as.TaskID).ControlRevision)), CancelTask{})
			} else {
				control := protocol.ControlPaused
				if mode == "takeover" {
					control = protocol.ControlHumanControl
				}
				op = h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: control}, rev(h.task(as.TaskID).ControlRevision)), SetControlMode{})
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			before := h.task(as.TaskID)
			if op := h.runAs(as, protocol.OpRunsRelease, preparationPayload(as), Release{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			after := h.task(as.TaskID)
			if after.ControlMode != before.ControlMode || after.Lifecycle.Terminal() != before.Lifecycle.Terminal() || after.BlockReason == protocol.BlockPreparationRequired || h.count("run_preparation_failures") != 1 || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatal("preparation fact undid intervention or lost final settlement", after)
			}
		})
	}
}
