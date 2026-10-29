package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) primaryOwner(taskID string) string {
	h.t.Helper()
	var id string
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		id, err = store.GetTaskIssueOwner(context.Background(), tx.Q(), taskID)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) assertNoWorkspaceBinding(taskID string) {
	h.t.Helper()
	err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		_, err := store.GetWorkspaceByTask(context.Background(), tx.Q(), taskID)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		h.t.Fatalf("workspace binding remains: %v", err)
	}
}

func TestWorkspaceOwnershipRejectsCrossItemReuseAndSecondaryLinks(t *testing.T) {
	h := newDeliverableHarness(t)
	sourceSpec := temporaryReportSpec()
	sourceSpec.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}
	source := h.createReportTask("source", sourceSpec)
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no source run")
	}
	primary := h.primaryOwner(source.TaskID)
	resource := h.resourceForTask(source.TaskID)
	if resource.OwnerWorkItemID != primary {
		t.Fatalf("resource owner is not creation primary: %+v", resource)
	}
	secondary := h.createWork(workPlan())
	link := h.exec(owner, h.env("work_items.link_task", workTarget(secondary.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: source.TaskID, StageIDs: []string{"investigate"}}, rev(secondary.Revision)), LinkIssueTask{})
	if link.Status != protocol.OpSucceeded {
		t.Fatal(link)
	}
	secondary = h.work(secondary.WorkItemID)
	if h.primaryOwner(source.TaskID) != primary || secondary.DefaultWorkspaceTaskID != "" {
		t.Fatal("secondary link changed ownership or automatically adopted its workspace")
	}
	reuse := temporaryReportSpec() // report-only/read-only intent still cannot cross ownership.
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
	beforeTasks, beforeItems := h.count("tasks"), h.count("work_items")
	for _, payload := range []createTaskPayload{
		{Title: "independent inspection", Spec: reuse},
		{Title: "secondary-item inspection", Spec: reuse, WorkItemID: secondary.WorkItemID, WorkItemRevision: secondary.Revision, StageIDs: []string{"investigate"}},
	} {
		op, _ := h.createTask(owner, payload)
		expectWorkCode(t, op, protocol.CodeInvalidRequest)
		if !strings.Contains(op.Error.Message, "same primary work item") {
			t.Fatal(op.Error)
		}
		if h.count("tasks") != beforeTasks || h.count("work_items") != beforeItems || h.resourceForTask(source.TaskID) != resource {
			t.Fatal("cross-item refusal partially created task/item or changed occupied resource")
		}
	}
	secondary.DefaultWorkspaceTaskID = source.TaskID
	op := h.exec(owner, h.env("work_items.update", workTarget(secondary.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: secondary.IssuePlan}, rev(secondary.Revision)), UpdateIssue{})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if h.work(secondary.WorkItemID).DefaultWorkspaceTaskID != "" {
		t.Fatal("cross-item default workspace was accepted")
	}
	follow := h.createReportTask("same-item continuation", reuse)
	if h.primaryOwner(follow.TaskID) != primary || h.resourceForTask(follow.TaskID).WorkspaceID != as.Workspace.ID {
		t.Fatal("same-item reuse did not retain ownership")
	}
}

func TestWorkspaceUnusedReuseCanDetachWithoutReleasingOtherTask(t *testing.T) {
	for _, mode := range []string{domain.WorkspaceNew, domain.WorkspaceTemporary} {
		t.Run(mode, func(t *testing.T) {
			h := newDeliverableHarness(t)
			source := h.createReportTask("source stays running", temporaryReportSpec())
			sourceRun, _, _ := h.claimDeliverableRun()
			reuse := temporaryReportSpec()
			reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
			pending := h.createReportTask("unstarted continuation", reuse)
			originalTask, originalSpec := h.task(pending.TaskID), h.storedSpec(pending.TaskID, 1)
			resource := h.resourceForTask(source.TaskID)
			primary := h.primaryOwner(pending.TaskID)
			next := originalSpec
			next.Workspace = domain.Workspace{Mode: mode}
			if mode == domain.WorkspaceNew {
				next.Workspace.Mounts = []domain.Mount{{Repo: "tend"}}
			}
			env := h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &next}, rev(originalTask.ControlRevision))
			op := h.exec(owner, env, UpdateIntent{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			replay := h.exec(owner, env, UpdateIntent{})
			if replay.Status != protocol.OpSucceeded || string(replay.Result) != string(op.Result) {
				t.Fatalf("same request replay changed detach result: %+v", replay)
			}
			h.assertNoWorkspaceBinding(pending.TaskID)
			if got := h.resourceForTask(source.TaskID); got != resource || got.OccupiedBy != source.TaskID {
				t.Fatalf("detach released or changed the source's resource: %+v", got)
			}
			if !reflect.DeepEqual(h.storedSpec(pending.TaskID, 1), originalSpec) || h.primaryOwner(pending.TaskID) != primary {
				t.Fatal("detach rewrote spec history or primary ownership")
			}
			changed := h.task(pending.TaskID)
			if changed.SpecVersion != 2 || changed.ControlRevision != originalTask.ControlRevision+1 || changed.WorkspaceAssigned {
				t.Fatalf("task revision/state after detach: %+v", changed)
			}
			expectWorkCode(t, h.exec(owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &next}, rev(originalTask.ControlRevision)), UpdateIntent{}), protocol.CodeStaleRevision)
			assigned, ok, _ := h.claimDeliverableRun()
			if !ok || assigned.TaskID != pending.TaskID || assigned.Workspace.ID == sourceRun.Workspace.ID || assigned.Workspace.Reused {
				t.Fatalf("independent task could not run beside source: %+v %v", assigned, ok)
			}
			if h.resourceForTask(pending.TaskID).OwnerWorkItemID != primary || h.resourceForTask(source.TaskID).OccupiedBy != source.TaskID {
				t.Fatal("fresh workspace has wrong owner or displaced the source")
			}
		})
	}
}

func TestWorkspaceUnusedRebindRollsBackOnLateWriteFailure(t *testing.T) {
	h := newDeliverableHarness(t)
	source := h.createReportTask("source", temporaryReportSpec())
	h.claimDeliverableRun()
	reuse := temporaryReportSpec()
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
	pending := h.createReportTask("pending", reuse)
	original, resource := h.task(pending.TaskID), h.resourceForTask(pending.TaskID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_rebind_action BEFORE INSERT ON plan_actions WHEN NEW.target_spec_version=2 BEGIN SELECT RAISE(ABORT,'injected action failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	next := temporaryReportSpec()
	_, err := h.app.Execute(t.Context(), owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &next}, rev(original.ControlRevision)), UpdateIntent{})
	if err == nil {
		t.Fatal("injected late write did not fail")
	}
	if h.task(pending.TaskID) != original || h.resourceForTask(pending.TaskID) != resource {
		t.Fatal("failed transaction left the task detached or advanced")
	}
	if h.count("task_specs") != 2 {
		t.Fatal("failed transaction retained a new spec")
	}
}

func TestWorkspaceRebindRejectsAnyRunHistoryOrUnresolvedOccupancy(t *testing.T) {
	for _, state := range []string{"starting", "running", "failed", "finished", "unknown", "occupied_without_run", "dispatched_without_run"} {
		t.Run(state, func(t *testing.T) {
			h := newDeliverableHarness(t)
			source := h.createReportTask("source", temporaryReportSpec())
			sourceRun, _, _ := h.claimDeliverableRun()
			reuse := temporaryReportSpec()
			reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
			pending := h.createReportTask("pending", reuse)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch state {
				case "occupied_without_run":
					_, err := tx.Exec(t.Context(), `UPDATE workspaces SET occupied_by=? WHERE workspace_id=?`, pending.TaskID, sourceRun.Workspace.ID)
					return err
				case "dispatched_without_run":
					_, err := tx.Exec(t.Context(), `UPDATE plan_actions SET status='dispatched' WHERE task_id=?`, pending.TaskID)
					return err
				default:
					_, err := tx.Exec(t.Context(), `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) SELECT ?,task_id,action_id,1,'implement','linux-1','run',?,1,1 FROM plan_actions WHERE task_id=? AND kind='implement'`, h.ids.New(protocol.PrefixRun), state, pending.TaskID)
					return err
				}
			}); err != nil {
				t.Fatal(err)
			}
			before, binding := h.task(pending.TaskID), h.resourceForTask(pending.TaskID)
			next := temporaryReportSpec()
			op := h.exec(owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &next}, rev(before.ControlRevision)), UpdateIntent{})
			expectWorkCode(t, op, protocol.CodeInvalidTransition)
			if h.task(pending.TaskID) != before || h.resourceForTask(pending.TaskID) != binding {
				t.Fatal("rejected rebind changed historical execution or ownership")
			}
		})
	}
}

func TestWorkspaceUnknownLegacyOwnerDeniesNewReuseButKeepsPinnedAgreement(t *testing.T) {
	h := newDeliverableHarness(t)
	source := h.createReportTask("source", temporaryReportSpec())
	as, _, _ := h.claimDeliverableRun()
	h.reportDirectory(as, t.TempDir())
	h.completeReport(as)
	h.releaseResource(source.TaskID, as.Workspace.ID)
	reuse := temporaryReportSpec()
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
	pending := h.createReportTask("already bound before upgrade", reuse)
	payload := h.sameItemReusePayload("new reuse", reuse)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		// Simulate a legacy resource with no verifiable v1 ownership, while
		// retaining its original physical and task-binding facts.
		_, err := tx.Exec(t.Context(), `UPDATE workspaces SET owner_work_item_id=NULL WHERE workspace_id=?`, as.Workspace.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op, _ := h.createTask(owner, payload)
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if !strings.Contains(op.Error.Message, "verified primary work item") {
		t.Fatal(op.Error)
	}
	edited := h.storedSpec(pending.TaskID, 1)
	edited.Scope = "Clarify the existing agreement without moving its resource"
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &edited}, rev(h.task(pending.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	claimed, ok, _ := h.claimDeliverableRun()
	if !ok || claimed.TaskID != pending.TaskID || claimed.Workspace.ID != as.Workspace.ID {
		t.Fatalf("legacy pinned claim was broken: %+v %v", claimed, ok)
	}
}

func TestWorkspaceLegacyCrossItemBindingCanDetachWithoutAdoptingOwnership(t *testing.T) {
	h := newDeliverableHarness(t)
	source := h.createReportTask("source", temporaryReportSpec())
	h.claimDeliverableRun()
	reuse := temporaryReportSpec()
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: source.TaskID}
	pending := h.createReportTask("legacy queued reuse", reuse)
	other := h.createWork(workPlan())
	// Model a pre-upgrade cross-item binding. Its task owner is immutable in
	// production, but the old binding must neither acquire the resource nor
	// prevent the owner from correcting a task that never ran.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE task_work_item_owners SET work_item_id=? WHERE task_id=?`, other.WorkItemID, pending.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	originalResource := h.resourceForTask(source.TaskID)
	edited := h.storedSpec(pending.TaskID, 1)
	edited.Scope = "Keep the original pinned agreement"
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &edited}, rev(h.task(pending.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A new consumer cannot launder the resource through this legacy task.
	payload := createTaskPayload{Title: "new consumer", Spec: reuse, WorkItemID: other.WorkItemID, WorkItemRevision: other.Revision, StageIDs: []string{"investigate"}}
	payload.Spec.Workspace.SourceTaskID = pending.TaskID
	op, _ = h.createTask(owner, payload)
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	edited.Workspace = domain.Workspace{Mode: domain.WorkspaceTemporary}
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &edited}, rev(h.task(pending.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.assertNoWorkspaceBinding(pending.TaskID)
	if h.primaryOwner(pending.TaskID) != other.WorkItemID || h.resourceForTask(source.TaskID) != originalResource {
		t.Fatal("correcting the legacy binding changed resource or primary ownership")
	}
}
