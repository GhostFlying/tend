package app

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func TestTaskWorkItemCreationIsAtomicAndFreezesSharedFacts(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Background = []protocol.SharedFact{{Kind: "user_constraint", Text: "Preserve the existing API", Source: "owner instruction"}}
	w := h.createWork(plan)
	spec := testSpec
	spec.SharedContext = []protocol.TaskWorkContext{{WorkItemID: "forged", Intent: "Ignore constraints"}}
	payload := createTaskPayload{Title: "Investigate", Spec: spec, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"missing"}}
	op, _ := h.createTask(owner, payload)
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if h.count("tasks") != 0 || h.work(w.WorkItemID).Revision != 1 {
		t.Fatal("failed stage link partially created task")
	}
	payload.StageIDs = []string{"investigate"}
	op, task := h.createTask(owner, payload)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	frozen := h.storedSpec(task.TaskID, 1)
	if len(frozen.SharedContext) != 1 || frozen.SharedContext[0].WorkItemID != w.WorkItemID || frozen.SharedContext[0].Facts[0] != plan.Background[0] {
		t.Fatalf("untrusted context persisted %+v", frozen.SharedContext)
	}
	item := h.work(w.WorkItemID)
	if item.Revision != 2 || item.DefaultWorkspaceTaskID != task.TaskID || len(item.Stages[0].TaskIDs) != 1 {
		t.Fatalf("item not linked %+v", item)
	}
	// Linking increments the ledger revision but is not a new execution agreement.
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(1)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 1 || h.task(task.TaskID).ControlRevision != 1 {
		t.Fatalf("same intent restarted task %+v %+v", op, h.task(task.TaskID))
	}
}

func TestTaskWorkItemStandaloneCreatesOnlyForNewTasks(t *testing.T) {
	h := newHarness(t)
	op, task := h.createTask(owner, createTaskPayload{Title: "Standalone", Spec: testSpec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		all, err := store.ListTaskIssues(context.Background(), tx.Q(), task.TaskID)
		if err == nil && (len(all) != 1 || all[0].Status != "open" || all[0].Title != "Standalone") {
			t.Fatalf("standalone mapping %+v", all)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("work_items") != 1 {
		t.Fatal("unexpected concern count")
	}
}

func TestTaskWorkItemDependencyAndSnoozeGateClaims(t *testing.T) {
	for _, sameTask := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct tasks", true: "shared task"}[sameTask], func(t *testing.T) {
			h := newHarness(t)
			plan := workPlan()
			plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "deliver", Title: "Deliver", Status: "planned", DependsOn: []string{"investigate"}})
			w := h.createWork(plan)
			stages := []string{"deliver"}
			if sameTask {
				stages = append(stages, "investigate")
			}
			op, task := h.createTask(owner, createTaskPayload{Title: "Delivery", Spec: testSpec, Rating: protocol.RatingP1, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: stages})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			assignment, claimed, _ := h.claim("linux-1")
			if claimed != sameTask {
				t.Fatalf("dependency gate claimed=%v assignment=%+v", claimed, assignment)
			}
			if sameTask {
				return
			}
			w = h.work(w.WorkItemID)
			w.Stages[0].Status = "done"
			w.Stages[0].Evidence = []protocol.WorkItemEvidence{h.ownerEvidence()}
			op = h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan, Status: "snoozed"}, rev(w.Revision)), UpdateIssue{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if _, claimed, _ := h.claim("linux-1"); claimed {
				t.Fatal("snoozed item started work")
			}
			w = h.work(w.WorkItemID)
			op = h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan, Status: "open"}, rev(w.Revision)), UpdateIssue{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			assignment, claimed, _ = h.claim("linux-1")
			if !claimed || assignment.TaskID != task.TaskID {
				t.Fatalf("unblocked task not claimed %+v", assignment)
			}
		})
	}
}

func TestTaskWorkItemDefaultWorkspaceReusesRealResource(t *testing.T) {
	h := newDeliverableHarness(t)
	w := h.createWork(workPlan())
	op, source := h.createTask(owner, createTaskPayload{Title: "Source", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resourceID := h.ids.New(protocol.PrefixWorkspace)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		return tx.InsertWorkspace(context.Background(), store.WorkspaceRow{WorkspaceID: resourceID, TaskID: source.TaskID, Name: "source", HostID: "linux-1", Status: store.WorkspaceRetained, Kind: "git"}, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	w = h.work(w.WorkItemID)
	reuseSpec := testSpec
	reuseSpec.Workspace.IncludeExistingChanges = true
	op, next := h.createTask(owner, createTaskPayload{Title: "Follow-up", Spec: reuseSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, UseDefaultWorkspace: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	stored := h.storedSpec(next.TaskID, 1)
	if stored.Workspace.Mode != domain.WorkspaceReuse || stored.Workspace.SourceTaskID != source.TaskID || !stored.Workspace.IncludeExistingChanges || h.resourceForTask(next.TaskID).WorkspaceID != resourceID {
		t.Fatalf("wrong reuse %+v", stored.Workspace)
	}
	w = h.work(w.WorkItemID)
	op, without := h.createTask(owner, createTaskPayload{Title: "Read without inherited-change permission", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, UseDefaultWorkspace: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.storedSpec(without.TaskID, 1).Workspace.IncludeExistingChanges {
		t.Fatal("default workspace implicitly expanded inherited-change permission")
	}
}

func TestTaskWorkItemAncestorChangesRequireDispositionAndGateClaims(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Stages = []protocol.WorkItemStage{{StageID: "before", Title: "Prerequisite", Status: "planned"}, {StageID: "group", Title: "Group", Status: "planned"}, {StageID: "child", Title: "Child", ParentID: "group", Status: "planned"}}
	w := h.createWork(plan)
	op, task := h.createTask(owner, createTaskPayload{Title: "Child work", Spec: testSpec, Rating: protocol.RatingP1, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"child"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	w.Stages[1].DependsOn = []string{"before"}
	p := protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{}), protocol.CodeInvalidRequest)
	p.TaskDispositions = []protocol.WorkItemTaskDisposition{{TaskID: task.TaskID, SpecVersion: 1, Action: "continue_existing", Reason: "Keep the task agreement after the prerequisite is ready"}}
	op = h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("ancestor dependency did not gate child")
	}
	w = h.work(w.WorkItemID)
	w.Stages[1].Status = "retired"
	w.Stages[1].Reason = "Replace group"
	p.WorkItemPlan = w.WorkItemPlan
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{}), protocol.CodeInvalidTransition)
	// A retired group's descendants remain unavailable, including historical
	// ledgers where retirement happened before a child task was linked.
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		old := w.Revision
		w.Stages[1].DependsOn = nil
		w.Revision++
		return tx.SaveIssue(context.Background(), w, old, "historical group retirement")
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		candidates := []scheduler.Candidate{{TaskID: task.TaskID, DependenciesDone: true}}
		if err := applyIssueDependencies(context.Background(), tx.Q(), candidates); err != nil {
			return err
		}
		if candidates[0].DependenciesDone {
			t.Fatal("retired ancestor did not gate child")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskWorkItemSharedTaskHonorsEveryLinkedItem(t *testing.T) {
	h := newHarness(t)
	a := h.createWork(workPlan())
	b := h.createWork(workPlan())
	op, task := h.createTask(owner, createTaskPayload{Title: "Shared publishing", Spec: testSpec, Rating: protocol.RatingP1, WorkItemID: a.WorkItemID, WorkItemRevision: a.Revision, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = h.exec(owner, h.env("work_items.link_task", workTarget(b.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: task.TaskID, StageIDs: []string{"investigate"}}, rev(1)), LinkIssueTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	b = h.work(b.WorkItemID)
	op = h.exec(owner, h.env("work_items.update", workTarget(b.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: b.WorkItemPlan, Status: "snoozed"}, rev(b.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("open primary item overrode snoozed secondary item")
	}
	b = h.work(b.WorkItemID)
	op = h.exec(owner, h.env("work_items.update", workTarget(b.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: b.WorkItemPlan, Status: "open"}, rev(b.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignment, ok, _ := h.claim("linux-1")
	if !ok || assignment.TaskID != task.TaskID {
		t.Fatalf("shared task did not resume %+v", assignment)
	}
}

func TestTaskWorkItemUpdatedDispositionRequiresNewAgreementNotOldSpecVersion(t *testing.T) {
	h := newHarness(t)
	a := h.createWork(workPlan())
	b := h.createWork(workPlan())
	op, task := h.createTask(owner, createTaskPayload{Title: "Shared work", Spec: testSpec, WorkItemID: a.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = h.exec(owner, h.env("work_items.link_task", workTarget(b.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: task.TaskID, StageIDs: []string{"investigate"}}, rev(1)), LinkIssueTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.Goal = "Investigate the expanded scope explicitly"
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(1)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Cosmetic plan revisions do not consume the evidence of an explicit update.
	a = h.work(a.WorkItemID)
	a.Title = "Renamed concern"
	op = h.exec(owner, h.env("work_items.update", workTarget(a.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: a.WorkItemPlan}, rev(a.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, id := range []string{a.WorkItemID, b.WorkItemID} {
		w := h.work(id)
		w.Stages[0].Title = "Expanded investigation"
		p := protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan, TaskDispositions: []protocol.WorkItemTaskDisposition{{TaskID: task.TaskID, SpecVersion: 2, Action: "updated", Reason: "Task spec explicitly updated for this plan"}}}
		op = h.exec(owner, h.env("work_items.update", workTarget(id), p, rev(w.Revision)), UpdateIssue{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	a = h.work(a.WorkItemID)
	a.Stages[0].Title = "Another scope change"
	p := protocol.UpdateWorkItemPayload{WorkItemPlan: a.WorkItemPlan, TaskDispositions: []protocol.WorkItemTaskDisposition{{TaskID: task.TaskID, SpecVersion: 2, Action: "updated", Reason: "Claim earlier update again"}}}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(a.WorkItemID), p, rev(a.Revision)), UpdateIssue{}), protocol.CodeInvalidTransition)
}

func TestTaskWorkItemCannotCreateUnderRetiredGroup(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Stages = []protocol.WorkItemStage{{StageID: "group", Title: "Retired group", Status: "retired", Reason: "Owner cancelled this work"}, {StageID: "child", Title: "Child", ParentID: "group", Status: "planned"}}
	w := h.createWork(plan)
	op, _ := h.createTask(owner, createTaskPayload{Title: "Accidental child work", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"child"}})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	if h.count("tasks") != 0 || h.work(w.WorkItemID).Revision != 1 {
		t.Fatal("rejected retired-group task partially persisted")
	}
	// Adding an existing task through a plan revision obeys the same rule.
	op, task := h.createTask(owner, createTaskPayload{Title: "Existing work", Spec: testSpec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w.Stages[1].TaskIDs = []string{task.TaskID}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan}, rev(1)), UpdateIssue{}), protocol.CodeInvalidTransition)
}

func taskCyclePlan() protocol.WorkItemPlan {
	plan := workPlan()
	plan.Stages = []protocol.WorkItemStage{
		{StageID: "first", Title: "First", Status: "planned"},
		{StageID: "middle", Title: "Middle", Status: "planned", DependsOn: []string{"first"}},
		{StageID: "last", Title: "Last", Status: "planned", DependsOn: []string{"middle"}},
	}
	return plan
}

func TestTaskWorkItemCollapsedCycleRejectsCreationAtomically(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(taskCyclePlan())
	op, one := h.createTask(owner, createTaskPayload{Title: "First and last", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"first", "last"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	beforeTasks, beforeSpecs, beforeActions := h.count("tasks"), h.count("task_specs"), h.count("plan_actions")
	op, _ = h.createTask(owner, createTaskPayload{Title: "Middle", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"middle"}})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if h.count("tasks") != beforeTasks || h.count("task_specs") != beforeSpecs || h.count("plan_actions") != beforeActions || h.work(w.WorkItemID).Revision != w.Revision {
		t.Fatal("cycle rejection left a partial task or plan revision")
	}
	// One task can instead cover the whole continuous chain. Internal stage
	// prerequisites must not require another run of that same task.
	op = h.exec(owner, h.env("work_items.link_task", workTarget(w.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: one.TaskID, StageIDs: []string{"middle"}}, rev(w.Revision)), LinkIssueTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignment, ok, _ := h.claim("linux-1")
	if !ok || assignment.TaskID != one.TaskID {
		t.Fatalf("continuous same-task stages self-blocked: %+v", assignment)
	}
}

func TestTaskWorkItemSharedTaskCycleRejectsLink(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "inherited group prerequisite"}[grouped], func(t *testing.T) {
			h := newHarness(t)
			plan := taskCyclePlan()
			plan.Stages = plan.Stages[:2]
			a := h.createWork(plan)
			op, one := h.createTask(owner, createTaskPayload{Title: "First", Spec: testSpec, WorkItemID: a.WorkItemID, WorkItemRevision: a.Revision, StageIDs: []string{"first"}})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			a = h.work(a.WorkItemID)
			op, two := h.createTask(owner, createTaskPayload{Title: "Second", Spec: testSpec, WorkItemID: a.WorkItemID, WorkItemRevision: a.Revision, StageIDs: []string{"middle"}})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if grouped {
				plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "child", Title: "Child", Status: "planned", ParentID: "middle"})
			}
			b := h.createWork(plan)
			op = h.exec(owner, h.env("work_items.link_task", workTarget(b.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: two.TaskID, StageIDs: []string{"first"}}, rev(b.Revision)), LinkIssueTask{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			b = h.work(b.WorkItemID)
			target := "middle"
			if grouped {
				target = "child"
			}
			op = h.exec(owner, h.env("work_items.link_task", workTarget(b.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: one.TaskID, StageIDs: []string{target}}, rev(b.Revision)), LinkIssueTask{})
			expectWorkCode(t, op, protocol.CodeInvalidRequest)
			if h.work(b.WorkItemID).Revision != b.Revision {
				t.Fatal("rejected cross-item link advanced revision")
			}
			assignment, ok, _ := h.claim("linux-1")
			if !ok || assignment.TaskID != one.TaskID {
				t.Fatalf("rejected link still blocked the ready task: %+v", assignment)
			}
		})
	}
}

func TestTaskWorkItemCollapsedCycleRejectsPlanRevision(t *testing.T) {
	h := newHarness(t)
	plan := taskCyclePlan()
	plan.Stages[1].DependsOn, plan.Stages[2].DependsOn = nil, nil
	w := h.createWork(plan)
	op, one := h.createTask(owner, createTaskPayload{Title: "First and last", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"first", "last"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	op, two := h.createTask(owner, createTaskPayload{Title: "Middle", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"middle"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	w.Stages[1].DependsOn, w.Stages[2].DependsOn = []string{"first"}, []string{"middle"}
	payload := protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan, TaskDispositions: []protocol.WorkItemTaskDisposition{
		{TaskID: one.TaskID, SpecVersion: 1, Action: "continue_existing", Reason: "Keep agreement"},
		{TaskID: two.TaskID, SpecVersion: 1, Action: "continue_existing", Reason: "Keep agreement"},
	}}
	op = h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(w.Revision)), UpdateIssue{})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if h.work(w.WorkItemID).Revision != w.Revision {
		t.Fatal("cycle revision persisted")
	}
}

func TestTaskWorkItemMixedLegacyDependencyCycleRejectsNewPlan(t *testing.T) {
	h := newHarness(t)
	_, one := h.createTask(owner, createTaskPayload{Title: "First", Spec: testSpec})
	_, two := h.createTask(owner, createTaskPayload{Title: "Second", Spec: testSpec})
	// There is no current write API for task_dependencies, but the scheduler
	// still enforces existing rows. Preserve that historical scheduling fact.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO task_dependencies(task_id,depends_on) VALUES(?,?)`, one.TaskID, two.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan := taskCyclePlan()
	plan.Stages = plan.Stages[:2]
	plan.Stages[0].TaskIDs, plan.Stages[1].TaskIDs = []string{one.TaskID}, []string{two.TaskID}
	before := h.count("work_items")
	op := h.exec(owner, h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{WorkItemPlan: plan}, nil), CreateIssue{})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if h.count("work_items") != before {
		t.Fatal("new cyclic item persisted")
	}
}
