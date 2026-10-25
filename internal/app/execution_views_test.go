package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (h *harness) executionTask(id string) protocol.TaskDetail {
	h.t.Helper()
	var view protocol.TaskDetail
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var err error
		view, err = views.Task(h.t.Context(), tx.Q(), id)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return view
}

func TestExecutionViewDistinguishesPlanQueueAndMixedWorkers(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Stages = []protocol.WorkItemStage{{StageID: "group", Title: "Group", Status: "active"}, {StageID: "child", ParentID: "group", Title: "Execution", Status: "active"}}
	item := h.createWork(plan)
	var ids []string
	for _, title := range []string{"first", "second", "third"} {
		item = h.work(item.WorkItemID)
		op, task := h.createTask(owner, createTaskPayload{Title: title, Spec: testSpec, WorkItemID: item.WorkItemID, WorkItemRevision: item.Revision, StageIDs: []string{"child"}})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		ids = append(ids, task.TaskID)
	}
	read := func() protocol.WorkItemView {
		t.Helper()
		var v protocol.WorkItemView
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.WorkItem(t.Context(), tx.Q(), item.WorkItemID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	initial := read()
	if initial.Execution.Queued != 3 || initial.Execution.Running != 0 || initial.Column != "active" {
		t.Fatalf("plan active is not worker execution: %+v", initial)
	}
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	events := h.count("events")
	current := read()
	if current.Execution.Running != 1 || current.Execution.Queued != 2 || len(current.StageExecution) != 2 {
		t.Fatalf("mixed execution %+v", current)
	}
	for _, stage := range current.StageExecution {
		if stage.Execution.Running != 1 || stage.Execution.Queued != 2 {
			t.Fatalf("stage aggregate %+v", stage)
		}
	}
	for _, task := range current.Tasks {
		if task.PrimaryWorkItemID != item.WorkItemID {
			t.Fatalf("missing primary owner %+v", task)
		}
		if task.TaskID != as.TaskID && (task.Execution.State != protocol.ExecutionQueued || task.Execution.QueueReason.Code != protocol.QueueReady) {
			t.Fatalf("queued %+v", task)
		}
	}
	if h.count("events") != events || h.work(item.WorkItemID).Stages[1].Status != "active" {
		t.Fatal("view mutated plan or events")
	}
	// A full host remains distinct from a busy workspace and from task BlockReason.
	h.claim("linux-1")
	var pending string
	for _, id := range ids {
		if h.executionTask(id).Execution.State == protocol.ExecutionQueued {
			pending = id
		}
	}
	queued := h.executionTask(pending)
	if queued.Execution.QueueReason.Code != protocol.QueueHostCapacity || queued.BlockReason != protocol.BlockNone {
		t.Fatalf("capacity %+v", queued)
	}
}

func TestExecutionViewAcceptanceIsQueuedUntilClaimedAndTerminalWins(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "verify", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{})
	waiting := h.executionTask(task.TaskID)
	if waiting.Lifecycle != protocol.TaskAwaitingAcceptance || waiting.Execution.State != protocol.ExecutionQueued || waiting.Execution.Role != protocol.RoleAccept {
		t.Fatalf("acceptance queued %+v", waiting)
	}
	acceptance, ok, _ := h.claim("linux-1")
	if !ok || acceptance.Role != protocol.RoleAccept {
		t.Fatal(acceptance)
	}
	h.runAs(acceptance, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if got := h.executionTask(task.TaskID).Execution; got.State != protocol.ExecutionAccepting || got.RunID != acceptance.RunID {
		t.Fatal(got)
	}
	// Cancellation can precede confirmation that the old worker stopped. The
	// task must still show its terminal business outcome, not active execution.
	op := h.exec(owner, h.env("tasks.cancel", taskTarget(task.TaskID), nil, rev(h.task(task.TaskID).ControlRevision)), CancelTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.executionTask(task.TaskID).Execution; got.State != protocol.ExecutionCancelled || got.RunID != "" {
		t.Fatal(got)
	}
}

func TestExecutionViewWorkspaceWaitIdentifiesOccupant(t *testing.T) {
	h := newDeliverableHarness(t)
	item := h.createWork(workPlan())
	op, first := h.createTask(owner, createTaskPayload{Title: "source", Spec: temporaryReportSpec(), WorkItemID: item.WorkItemID, WorkItemRevision: item.Revision, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no source run")
	}
	h.reportDirectory(as, t.TempDir())
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "needs owner decision"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if firstView := h.executionTask(first.TaskID); firstView.Execution.State != protocol.ExecutionWaitingOwner || firstView.BlockReason != protocol.BlockWaitingDecision {
		t.Fatalf("occupant waiting for decision %+v", firstView)
	}
	item = h.work(item.WorkItemID)
	spec := temporaryReportSpec()
	spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: first.TaskID}
	op, next := h.createTask(owner, createTaskPayload{Title: "follow up", Spec: spec, WorkItemID: item.WorkItemID, WorkItemRevision: item.Revision, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	waiting := h.executionTask(next.TaskID)
	reason := waiting.Execution.QueueReason
	if waiting.Execution.State != protocol.ExecutionQueued || reason.Code != protocol.QueueWorkspaceBusy || reason.TaskID != first.TaskID || reason.ResourceID != as.Workspace.ID || reason.HostID != "linux-1" {
		t.Fatalf("workspace wait %+v %+v", waiting.Execution, reason)
	}
	if waiting.WorkspaceState.OwnerWorkItemID != item.WorkItemID {
		t.Fatalf("workspace owner %+v", waiting.WorkspaceState)
	}
	if _, claimed, _ := h.claimDeliverableRun(); claimed {
		t.Fatal("view and claim eligibility disagree")
	}
}

func TestExecutionViewDependencyUsesClaimRules(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "after", Title: "After", Status: "active", DependsOn: []string{"investigate"}})
	item := h.createWork(plan)
	_, task := h.createTask(owner, createTaskPayload{Title: "dependent", Spec: testSpec, WorkItemID: item.WorkItemID, WorkItemRevision: item.Revision, StageIDs: []string{"after"}})
	if got := h.executionTask(task.TaskID).Execution; got.State != protocol.ExecutionQueued || got.QueueReason.Code != protocol.QueueDependency {
		t.Fatal(got)
	}
	if _, claimed, _ := h.claim("linux-1"); claimed {
		t.Fatal("dependency claim diverged")
	}
	// Historical direct task dependencies also expose the blocking task.
	_, other := h.createTask(owner, createTaskPayload{Title: "prerequisite", Spec: testSpec})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO task_dependencies(task_id,depends_on) VALUES(?,?)`, task.TaskID, other.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.executionTask(task.TaskID).Execution.QueueReason; got.Code != protocol.QueueDependency || got.TaskID != other.TaskID {
		t.Fatal(got)
	}
}

func TestExecutionViewWorkerOwnerWaitAndResume(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	question := askQuestion(t, h, as, scopeQuestion)
	if got := h.executionTask(as.TaskID).Execution.State; got != protocol.ExecutionWaitingOwner {
		t.Fatal(got)
	}
	if op := answerQuestion(t, h, question, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.executionTask(as.TaskID).Execution.State; got != protocol.ExecutionRunning {
		t.Fatal(got)
	}
	op := h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(as.TaskID).ControlRevision)), SetControlMode{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.executionTask(as.TaskID).Execution.State; got != protocol.ExecutionRunning {
		t.Fatal(got)
	}
}

func TestExecutionViewHostAdmissionAndRequirements(t *testing.T) {
	for _, mode := range []string{"maintenance", "offline", "requirements"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			runtime := testRuntime("execution-view")
			h.versionedHello("linux-1", runtime)
			_, task := h.createTask(owner, createTaskPayload{Title: "pending", Spec: testSpec})
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch mode {
				case "maintenance":
					return tx.PutHostControl(t.Context(), "linux-1", protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: 1}, h.clock.Now().UnixMilli())
				case "offline":
					return tx.PutHostRuntime(t.Context(), "linux-1", store.HostRuntimeRow{Runtime: *runtime, Online: false})
				default:
					_, err := tx.Exec(t.Context(), `UPDATE hosts SET repos_json='[]' WHERE host_id='linux-1'`)
					return err
				}
			}); err != nil {
				t.Fatal(err)
			}
			expected := protocol.QueueHostUnavailable
			if mode == "requirements" {
				expected = protocol.QueueHostRequirements
			}
			if got := h.executionTask(task.TaskID).Execution.QueueReason; got.Code != expected {
				t.Fatalf("reason %+v want %s", got, expected)
			}
		})
	}
}

func TestExecutionViewNonblockingDecisionDoesNotHideWorker(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "continue working", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	op := h.exec(owner, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{TaskID: task.TaskID, Question: "What should a later task investigate?"}, nil), OpenDecision{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	events := h.count("events")
	view := h.executionTask(task.TaskID)
	if view.Execution.State != protocol.ExecutionRunning || view.Execution.RunID != as.RunID {
		t.Fatalf("nonblocking decision hid live worker: %+v", view.Execution)
	}
	if h.count("events") != events {
		t.Fatal("execution read changed ledger")
	}
}

func TestExecutionViewHistoricalDecisionDoesNotHideNewSpecRun(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "revised investigation", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "failed"}, Release{})
	next := testSpec
	next.Scope = "Revised scope"
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &next}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The old decision remains history; a pending new spec is still queued.
	queued := h.executionTask(task.TaskID)
	if queued.SpecVersion != 2 || queued.Execution.State != protocol.ExecutionQueued {
		t.Fatalf("historical decision hid new queue: %+v", queued)
	}
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no new claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	view := h.executionTask(task.TaskID)
	if view.Execution.State != protocol.ExecutionRunning || view.Execution.RunID != as.RunID {
		t.Fatalf("historical decision hid new worker: %+v", view.Execution)
	}
}

func TestExecutionViewUnknownWorkerQuestionRemainsUnknown(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			h, as, _ := questionRun(t, role)
			question := askQuestion(t, h, as, scopeQuestion)
			before := h.executionTask(as.TaskID).Execution
			if before.State != protocol.ExecutionWaitingOwner || before.RunID != as.RunID || before.Role != role {
				t.Fatalf("live worker question: %+v", before)
			}
			h.clock.Advance(DefaultLeaseTTL + time.Second)
			if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
				t.Fatalf("expired %d runs: %v", n, err)
			}
			events := h.count("events")
			view := h.executionTask(as.TaskID)
			if view.Execution.State != protocol.ExecutionUnknown || view.Execution.RunID != as.RunID {
				t.Fatalf("question concealed unknown worker: %+v", view.Execution)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				decision, err := store.GetDecision(t.Context(), tx.Q(), question.DecisionID)
				if err == nil && decision.Status != store.DecisionOpen {
					t.Fatalf("view changed decision: %+v", decision)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if h.count("events") != events {
				t.Fatal("view changed ledger")
			}
		})
	}
}

func TestExecutionViewControlIntentDoesNotOverrideObservedWorker(t *testing.T) {
	for _, mode := range []protocol.ControlMode{protocol.ControlPaused, protocol.ControlHumanControl} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t)
			_, task := h.createTask(owner, createTaskPayload{Title: "controlled worker", Spec: testSpec})
			as, claimed, _ := h.claim("linux-1")
			if !claimed {
				t.Fatal("no claim")
			}
			h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			op := h.exec(owner, h.env("control.set_mode", taskTarget(task.TaskID), protocol.SetControlModePayload{Mode: mode}, rev(h.task(task.TaskID).ControlRevision)), SetControlMode{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			running := h.executionTask(task.TaskID)
			if running.ControlMode != mode || running.Execution.State != protocol.ExecutionRunning {
				t.Fatalf("control intent concealed observed execution: %+v", running)
			}
			h.clock.Advance(DefaultLeaseTTL + time.Second)
			if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
				t.Fatalf("expire %d: %v", n, err)
			}
			unknown := h.executionTask(task.TaskID)
			if unknown.Execution.State != protocol.ExecutionUnknown || unknown.Execution.RunID != as.RunID {
				t.Fatalf("control intent concealed unknown: %+v", unknown.Execution)
			}
			// A reconnect reports the worker was fenced. Only this observation proves
			// the allocated worker is interrupted rather than merely asked to pause.
			h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
			paused := h.executionTask(task.TaskID)
			if paused.Execution.State != protocol.ExecutionPaused || paused.Execution.RunID != as.RunID {
				t.Fatalf("confirmed interruption: %+v", paused.Execution)
			}
		})
	}
}

func TestExecutionViewPausedTaskWithoutRunDoesNotQueue(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "pause before dispatch", Spec: testSpec})
	op := h.exec(owner, h.env("control.set_mode", taskTarget(task.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(task.TaskID).ControlRevision)), SetControlMode{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	view := h.executionTask(task.TaskID)
	if view.Execution.State != protocol.ExecutionPaused || view.Execution.RunID != "" {
		t.Fatalf("paused task without a run: %+v", view.Execution)
	}
}

func TestExecutionViewSupersededWorkerRemainsVisibleUntilStopped(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "update active work", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	next := testSpec
	next.Scope = "new scope while old worker is unresolved"
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &next}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	updated := h.executionTask(task.TaskID)
	if updated.SpecVersion != 2 || updated.Execution.State != protocol.ExecutionRunning || updated.Execution.RunID != as.RunID {
		t.Fatalf("old spec still running: %+v", updated)
	}
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
		t.Fatalf("expire %d: %v", n, err)
	}
	if got := h.executionTask(task.TaskID).Execution; got.State != protocol.ExecutionUnknown || got.RunID != as.RunID {
		t.Fatalf("old spec unknown hidden: %+v", got)
	}
}

func TestExecutionViewExhaustedBudgetWaitsForOwner(t *testing.T) {
	h := newHarness(t)
	spec := testSpec
	spec.MaxImplementAttempts = 1
	_, task := h.createTask(owner, createTaskPayload{Title: "one attempt", Spec: spec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{})
	acc, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no acceptance")
	}
	h.runAs(acc, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	got := h.executionTask(task.TaskID)
	if got.BlockReason != protocol.BlockBudgetExhausted || got.Execution.State != protocol.ExecutionWaitingOwner {
		t.Fatalf("budget exhaustion hidden: %+v", got)
	}
}

func TestExecutionViewUnusableAcceptanceWaitsForOwner(t *testing.T) {
	h := newHarness(t)
	task, acc := h.toAcceptanceClaim()
	h.runAs(acc, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	got := h.executionTask(task)
	if got.BlockReason != protocol.BlockAcceptanceEnvUnavailable || got.Execution.State != protocol.ExecutionWaitingOwner {
		t.Fatalf("blocked acceptance looks queued: %+v", got)
	}
}

func TestExecutionViewStageDependencyLinksOnlyUnfinishedTask(t *testing.T) {
	for _, parentDependency := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "ancestor"}[parentDependency], func(t *testing.T) {
			h := newHarness(t)
			plan := workPlan()
			plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "after", Title: "After", Status: "active", DependsOn: []string{"investigate"}})
			dependentStage := "after"
			if parentDependency {
				plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "child", Title: "Child", ParentID: "after", Status: "active"})
				dependentStage = "child"
			}
			item := h.createWork(plan)
			_, before := h.createTask(owner, createTaskPayload{Title: "before", Spec: testSpec, WorkItemID: item.WorkItemID, WorkItemRevision: item.Revision, StageIDs: []string{"investigate"}})
			item = h.work(item.WorkItemID)
			_, after := h.createTask(owner, createTaskPayload{Title: "after", Spec: testSpec, WorkItemID: item.WorkItemID, WorkItemRevision: item.Revision, StageIDs: []string{dependentStage}})
			reason := h.executionTask(after.TaskID).Execution.QueueReason
			if reason.Code != protocol.QueueDependency || reason.TaskID != before.TaskID {
				t.Fatalf("missing prerequisite link: %+v", reason)
			}
			// Completing the task does not complete the owner's stage. Keep the
			// dependency wait, but stop blaming the now-completed task for it.
			as, ok, _ := h.claim("linux-1")
			if !ok || as.TaskID != before.TaskID {
				t.Fatal(as)
			}
			h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(before.TaskID).WorkspaceName), SubmitArtifact{})
			acc, ok, _ := h.claim("linux-1")
			if !ok || acc.Role != protocol.RoleAccept {
				t.Fatal(acc)
			}
			h.runAs(acc, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			if op := h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			reason = h.executionTask(after.TaskID).Execution.QueueReason
			if reason.Code != protocol.QueueDependency || reason.TaskID != "" {
				t.Fatalf("completed task falsely blamed: %+v", reason)
			}
			if h.work(item.WorkItemID).Stages[0].Status != "planned" {
				t.Fatal("view completed the plan")
			}
		})
	}
}
