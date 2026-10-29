package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func workTarget(id string) protocol.Target { return protocol.Target{Type: "work_item", ID: id} }
func workPlan() protocol.IssuePlan {
	return protocol.IssuePlan{Title: "Improve search", Intent: "Investigate and implement the agreed search improvements", Stages: []protocol.IssueStage{{StageID: "investigate", Title: "Investigate", Status: "planned"}}}
}
func (h *harness) createWork(plan protocol.IssuePlan) protocol.WorkItem {
	h.t.Helper()
	op := h.exec(owner, h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil), CreateIssue{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("create work: %+v", op)
	}
	var w protocol.WorkItem
	if err := json.Unmarshal(op.Result, &w); err != nil {
		h.t.Fatal(err)
	}
	return w
}
func (h *harness) work(id string) protocol.WorkItem {
	h.t.Helper()
	var w protocol.WorkItem
	err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		w, err = store.GetIssue(context.Background(), tx.Q(), id)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}
func (h *harness) ownerEvidence() protocol.IssueEvidence {
	h.t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "The requested responsibility ends after the verified result. Remaining optional suggestions are not commitments."}, nil), SubmitMessage{})
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || result.MessageID == "" {
		h.t.Fatalf("owner message: %+v %v", op, err)
	}
	return protocol.IssueEvidence{Kind: "owner_decision", Reference: result.MessageID, Summary: "Owner defined the responsibility endpoint"}
}
func expectWorkCode(t *testing.T, op protocol.Operation, code protocol.Code) {
	t.Helper()
	if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != code {
		t.Fatalf("got %+v, want %s", op, code)
	}
}

func TestWorkItemsRevisionDedupAndHistory(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	env := h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil)
	first := h.exec(mainAgent, env, CreateIssue{})
	second := h.exec(mainAgent, env, CreateIssue{})
	if first.Status != protocol.OpSucceeded || string(first.Result) != string(second.Result) || h.count("work_items") != 1 {
		t.Fatalf("dedup %+v %+v", first, second)
	}
	var w protocol.WorkItem
	_ = json.Unmarshal(first.Result, &w)
	w.Title = "Improved search plan"
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(1)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(1)), UpdateIssue{}), protocol.CodeStaleRevision)
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		old, err := store.GetIssueRevision(context.Background(), tx.Q(), w.WorkItemID, 1)
		if err == nil && old.Title != plan.Title {
			t.Fatal("history overwritten")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := h.app.Execute(context.Background(), runPrincipal(h.ids.New(protocol.PrefixRun), protocol.RoleImplement), h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil), CreateIssue{})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("worker permission: %v", err)
	}
}

func TestWorkItemsSharedTaskLinksAndPlanAgreement(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "investigation", Spec: testSpec})
	w := h.createWork(workPlan())
	other := h.createWork(workPlan())
	for _, item := range []protocol.WorkItem{w, other} {
		op := h.exec(owner, h.env("work_items.link_task", workTarget(item.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: task.TaskID, StageIDs: []string{"investigate"}}, rev(1)), LinkIssueTask{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		all, err := store.ListTaskIssues(context.Background(), tx.Q(), task.TaskID)
		if err == nil && len(all) != 3 {
			t.Fatalf("shared task has %d items", len(all))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w = h.work(w.WorkItemID)
	w.Stages[0].Title = "Broader investigation"
	payload := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(2)), UpdateIssue{}), protocol.CodeInvalidRequest)
	payload.TaskDispositions = []protocol.IssueTaskDisposition{{TaskID: task.TaskID, Action: "continue_existing", SpecVersion: 1, Reason: "Keep the original investigation agreement; expansion is a later task"}}
	op := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(2)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.storedSpec(task.TaskID, 1); got.Goal != testSpec.Goal || h.task(task.TaskID).SpecVersion != 1 {
		t.Fatal("plan silently changed task agreement")
	}
	w = h.work(w.WorkItemID)
	w.Stages[0].TaskIDs = nil
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(3)), UpdateIssue{}), protocol.CodeInvalidRequest)
}

func TestWorkItemsExplicitCloseReopenAndUnresolvedCommitments(t *testing.T) {
	h := newHarness(t)
	evidence := h.ownerEvidence()
	plan := workPlan()
	plan.Stages = nil
	plan.FollowOns = []protocol.IssueFollowOn{{FollowOnID: "promise", Kind: "commitment", Text: "Provide follow-up", Source: evidence.Reference, Status: "open"}}
	w := h.createWork(plan)
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "completed", Reason: "Responsibility ended", Evidence: []protocol.IssueEvidence{evidence}}}
	expectWorkCode(t, h.exec(mainAgent, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(1)), CloseIssue{}), protocol.CodeInvalidTransition)
	// Removing or downgrading the commitment cannot bypass the guard.
	for _, change := range []string{"remove", "downgrade"} {
		p := w.IssuePlan
		if change == "remove" {
			p.FollowOns = nil
		} else {
			p.FollowOns = append([]protocol.IssueFollowOn(nil), p.FollowOns...)
			p.FollowOns[0].Kind = "suggestion"
		}
		expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: p}, rev(1)), UpdateIssue{}), protocol.CodeInvalidRequest)
	}
	w.FollowOns[0].Status = "transferred"
	w.FollowOns[0].TransferredTo = "Owner-maintained follow-up list"
	w.FollowOns[0].Evidence = []protocol.IssueEvidence{evidence}
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(1)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.work(w.WorkItemID).Status != "open" {
		t.Fatal("zero tasks inferred closure")
	}
	op = h.exec(mainAgent, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(2)), CloseIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = h.exec(mainAgent, h.env("work_items.reopen", workTarget(w.WorkItemID), protocol.ReopenWorkItemPayload{Reason: "Owner requested another iteration"}, rev(3)), ReopenIssue{})
	if op.Status != protocol.OpSucceeded || h.work(w.WorkItemID).Status != "open" || h.count("work_item_revisions") != 4 {
		t.Fatal(op)
	}
}

func TestWorkItemsCloseGuardsAndEvidence(t *testing.T) {
	h := newHarness(t)
	evidence := h.ownerEvidence()
	_, task := h.createTask(owner, createTaskPayload{Title: "work", Spec: testSpec})
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{task.TaskID}
	w := h.createWork(plan)
	w.Stages[0].Status = "done"
	w.Stages[0].Evidence = []protocol.IssueEvidence{evidence}
	changed := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan, TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task.TaskID, SpecVersion: 1, Action: "continue_existing", Reason: "Owner considers stage sufficient; let existing task settle"}}}, rev(1)), UpdateIssue{})
	if changed.Status != protocol.OpSucceeded {
		t.Fatal(changed)
	}
	w = h.work(w.WorkItemID)
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "declined", Reason: "Explicit endpoint", Evidence: []protocol.IssueEvidence{evidence}}}
	closeWork := func() protocol.Operation {
		return h.exec(owner, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(w.Revision)), CloseIssue{})
	}
	expectWorkCode(t, closeWork(), protocol.CodeInvalidTransition)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE tasks SET lifecycle='cancelled' WHERE task_id=?`, task.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// An unsettled run remains a guard even when the task row is terminal.
	var action store.ActionRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		actions, err := store.ListActions(context.Background(), tx.Q(), task.TaskID)
		if err == nil && len(actions) > 0 {
			action = actions[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runID := h.ids.New(protocol.PrefixRun)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,1,'implement','stopped','unknown',0,0)`, runID, task.TaskID, action.ActionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, closeWork(), protocol.CodeInvalidTransition)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE runs SET observed_state='interrupted' WHERE run_id=?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closure.Closure.Evidence[0].Reference = "invented"
	expectWorkCode(t, closeWork(), protocol.CodeInvalidRequest)
	closure.Closure.Evidence[0] = evidence
	op := closeWork()
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestWorkItemsSourceCommitDedup(t *testing.T) {
	h := newHarness(t)
	source := h.ownerEvidence().Reference
	p := protocol.CreateWorkItemPayload{IssuePlan: workPlan(), SourceMessageID: source, CommitSeq: 1}
	op := h.exec(mainAgent, h.env("work_items.create", workTarget(""), p, nil), CreateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	expectWorkCode(t, h.exec(mainAgent, h.env("work_items.create", workTarget(""), p, nil), CreateIssue{}), protocol.CodeDuplicateEffect)
	if h.count("work_items") != 1 || h.count("work_item_revisions") != 1 {
		t.Fatal("replay mutated ledger")
	}
}

func TestWorkItemsUncertainOperationAndOpenDecisionPreventClosure(t *testing.T) {
	h := newHarness(t)
	evidence := h.ownerEvidence()
	op, task := h.createTask(owner, createTaskPayload{Title: "Finished work", Spec: testSpec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE tasks SET lifecycle='completed' WHERE task_id=?`, task.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan := workPlan()
	plan.Stages[0].Status = "done"
	plan.Stages[0].Evidence = []protocol.IssueEvidence{evidence}
	plan.Stages[0].TaskIDs = []string{task.TaskID}
	w := h.createWork(plan)
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "completed", Reason: "Owner endpoint", Evidence: []protocol.IssueEvidence{evidence}}}
	closeWork := func() protocol.Operation {
		return h.exec(owner, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(1)), CloseIssue{})
	}
	decisionID := h.ids.New(protocol.PrefixDecision)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO decisions(decision_id,task_id,question,options_json,scope,version,status,created_at,updated_at) VALUES(?,?,'Resolve issue','[]','task',1,'open',0,0)`, decisionID, task.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, closeWork(), protocol.CodeInvalidTransition)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.Exec(context.Background(), `UPDATE decisions SET status='answered' WHERE decision_id=?`, decisionID); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `UPDATE request_records SET status='unknown' WHERE request_id=?`, op.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, closeWork(), protocol.CodeInvalidTransition)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE request_records SET status='succeeded' WHERE request_id=?`, op.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if result := closeWork(); result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
}

func TestWorkItemsDirectOwnerEvidenceFreezesRequestIdentity(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Stages[0].Status = "done"
	plan.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: "owner_decision", Reference: "current_request", Summary: "Owner confirms stage result"}}
	env := h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil)
	op := h.exec(owner, env, CreateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var w protocol.WorkItem
	_ = json.Unmarshal(op.Result, &w)
	if w.Stages[0].Evidence[0].Reference != env.RequestID {
		t.Fatal("owner evidence lacks durable request identity")
	}
	w.Title = "Renamed"
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(1)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	plan.Stages[0].Evidence[0].Reference = "current_request"
	expectWorkCode(t, h.exec(mainAgent, h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil), CreateIssue{}), protocol.CodeInvalidRequest)
}

func TestWorkItemsDeliveryEvidenceAndPlanUpdatesPreserveObservations(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "review", Provider: "github", Repository: "example/repo", Number: 1}}
	w := h.createWork(plan)
	w.Stages[0].Status = "done"
	w.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: "delivery", Reference: "review", Summary: "Provider confirms merge"}}
	payload := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(1)), UpdateIssue{}), protocol.CodeInvalidRequest)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutDeliveryObservation(context.Background(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: w.WorkItemID, DeliveryID: "review", Generation: 3, DeliverySnapshot: protocol.DeliverySnapshot{State: "merged"}, CheckedAt: h.clock.Now().UnixMilli()}})
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(1)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		rows, err := store.ListDeliveryObservations(context.Background(), tx.Q(), w.WorkItemID)
		if err == nil && (len(rows) != 1 || rows[0].Generation != 3 || rows[0].State != "merged") {
			t.Fatalf("plan overwrote observation %+v", rows)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "completed", Reason: "Merged", Evidence: w.Stages[0].Evidence}}
	expectWorkCode(t, h.exec(owner, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(2)), CloseIssue{}), protocol.CodeInvalidTransition)
}

func TestWorkItemsStageAcceptsOnlyLinkedIndependentAcceptance(t *testing.T) {
	h := newDeliverableHarness(t)
	task := h.createReportTask("Research", temporaryReportSpec())
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no assignment")
	}
	h.reportDirectory(as, t.TempDir())
	h.completeReport(as)
	var item protocol.WorkItem
	var acceptanceID string
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		items, err := store.ListTaskIssues(context.Background(), tx.Q(), task.TaskID)
		if err != nil {
			return err
		}
		item = items[0]
		return tx.Q().QueryRowContext(context.Background(), `SELECT acceptance_id FROM acceptances WHERE task_id=?`, task.TaskID).Scan(&acceptanceID)
	}); err != nil {
		t.Fatal(err)
	}
	evidence := protocol.IssueEvidence{Kind: "acceptance", Reference: acceptanceID, Summary: "Independent acceptance verified the report"}
	item.Stages[0].Status = "done"
	item.Stages[0].Evidence = []protocol.IssueEvidence{evidence}
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(item.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: item.IssuePlan}, rev(item.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.work(item.WorkItemID).Status != "open" {
		t.Fatal("research completion closed continuing responsibility")
	}
	unrelated := workPlan()
	unrelated.Stages[0].Status = "done"
	unrelated.Stages[0].Evidence = []protocol.IssueEvidence{evidence}
	expectWorkCode(t, h.exec(owner, h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: unrelated}, nil), CreateIssue{}), protocol.CodeInvalidRequest)
}

func TestWorkItemsRetiringActiveStageCannotLeaveOldWorkRunning(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	op, task := h.createTask(owner, createTaskPayload{Title: "Active agreement", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	w.Stages[0].Status = "retired"
	w.Stages[0].Reason = "Replace with a new stage"
	p := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan, TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task.TaskID, SpecVersion: 1, Action: "continue_existing", Reason: "Keep original agreement"}}}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{}), protocol.CodeInvalidTransition)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE tasks SET lifecycle='cancelled' WHERE task_id=?`, task.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.TaskDispositions = nil
	op = h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestWorkItemsDeliveryEvidenceFreezesVerifiedSnapshot(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "review", Provider: "github", Repository: "example/repo", Number: 1}}
	w := h.createWork(plan)
	fake := protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: "invented"}, WorkItemID: w.WorkItemID, DeliveryID: "review", Generation: 999, CheckedAt: 999}
	evidence := protocol.IssueEvidence{Kind: "delivery", Reference: "review", Summary: "Verified initial review state", Observation: &fake}
	w.Stages[0].Status = "done"
	w.Stages[0].Evidence = []protocol.IssueEvidence{evidence}
	payload := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}
	expectWorkCode(t, h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(1)), UpdateIssue{}), protocol.CodeInvalidRequest)
	put := func(generation int64, state, head string) {
		t.Helper()
		if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
			return tx.PutDeliveryObservation(context.Background(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: state, HeadSHA: head}, WorkItemID: w.WorkItemID, DeliveryID: "review", Generation: generation, CheckedAt: h.clock.Now().UnixMilli() + generation}})
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(3, "merged", "verified-head")
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), payload, rev(1)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	frozen := w.Stages[0].Evidence[0].Observation
	if frozen == nil || frozen.Generation != 3 || frozen.HeadSHA != "verified-head" || frozen.CheckedAt != h.clock.Now().UnixMilli()+3 {
		t.Fatalf("caller forged provider proof: %+v", frozen)
	}
	// Later provider facts cannot rewrite the evidence in an older revision.
	put(4, "open", "new-head")
	w.Title = "Updated title"
	w.Stages[0].Evidence[0].Observation = &fake
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(2)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	if got := w.Stages[0].Evidence[0].Observation; got == nil || got.Generation != 3 || got.HeadSHA != "verified-head" {
		t.Fatalf("old evidence was not preserved %+v", got)
	}
	// Omitting a server-owned snapshot also retains identity without a revision.
	w.Stages[0].Evidence[0].Observation = nil
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(3)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded || h.work(w.WorkItemID).Revision != 3 {
		t.Fatalf("unchanged evidence advanced revision %+v", op)
	}
	w = h.work(w.WorkItemID)
	fresh := evidence
	fresh.Summary = "Verified newer review state"
	w.Stages[0].Evidence = append(w.Stages[0].Evidence, fresh)
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(3)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	if w.Stages[0].Evidence[0].Observation.Generation != 3 || w.Stages[0].Evidence[1].Observation.Generation != 4 {
		t.Fatalf("new evidence did not freeze newer observation %+v", w.Stages[0].Evidence)
	}
	ownerFact := h.ownerEvidence()
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "completed", Reason: "Explicit owner endpoint", Evidence: []protocol.IssueEvidence{ownerFact, evidence}}}
	op = h.exec(mainAgent, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(4)), CloseIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.work(w.WorkItemID).Closure.Evidence[1].Observation; got == nil || got.Generation != 3 {
		t.Fatalf("closure reread newer evidence %+v", got)
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		old, err := store.GetIssueRevision(context.Background(), tx.Q(), w.WorkItemID, 2)
		if err == nil && old.Stages[0].Evidence[0].Observation.Generation != 3 {
			t.Fatal("historical observation overwritten")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemsOwnerExternalCloseUsesExistingOwnerEvidence(t *testing.T) {
	h := newHarness(t)
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "My review account is release-owner (account ID 42). If I close this PR myself, that ends this work item; do not ask again."}, nil), SubmitMessage{})
	var source struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &source); err != nil || source.MessageID == "" {
		t.Fatal(op)
	}
	plan := workPlan()
	plan.Stages = nil
	plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "review", Provider: "github", Repository: "example/repo", Number: 1}}
	w := h.createWork(plan)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutDeliveryObservation(context.Background(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: "closed", ClosedAt: "2026-01-02T03:04:05Z", ClosedBy: &protocol.DeliveryActor{ID: "42", Login: "release-owner"}}, WorkItemID: w.WorkItemID, DeliveryID: "review", Generation: 1, CheckedAt: h.clock.Now().UnixMilli()}})
	}); err != nil {
		t.Fatal(err)
	}
	decisionsBefore := h.count("decisions")
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "declined", Reason: "The owner closed the PR, ending the responsibility as previously instructed", Evidence: []protocol.IssueEvidence{{Kind: "owner_decision", Reference: source.MessageID, Summary: "Existing owner instruction identifies account 42 and says their close ends responsibility"}, {Kind: "delivery", Reference: "review", Summary: "Provider reports that account 42 closed the PR"}}}}
	op = h.exec(mainAgent, h.env("work_items.close", workTarget(w.WorkItemID), closure, rev(1)), CloseIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	closed := h.work(w.WorkItemID)
	if closed.Closure.Evidence[1].Observation.ClosedBy.ID != "42" || closed.Closure.Evidence[0].Reference != source.MessageID {
		t.Fatalf("closure lost identity provenance %+v", closed.Closure)
	}
	if h.count("decisions") != decisionsBefore {
		t.Fatal("existing owner evidence unexpectedly requested fresh approval")
	}
}

func TestWorkItemsRetirementWaitsForCancelledTaskStopProof(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	op, task := h.createTask(owner, createTaskPayload{Title: "Pending stop", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: 1, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runID := h.startRun(task.TaskID, protocol.ActionImplement)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.Exec(context.Background(), `UPDATE tasks SET lifecycle='cancelled' WHERE task_id=?`, task.TaskID); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `UPDATE runs SET desired_state='stopped',observed_state='unknown' WHERE run_id=?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w = h.work(w.WorkItemID)
	w.Stages[0].Status = "retired"
	w.Stages[0].Reason = "Task cancelled"
	p := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}
	expectWorkCode(t, h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{}), protocol.CodeInvalidTransition)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE runs SET observed_state='interrupted' WHERE run_id=?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op = h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
