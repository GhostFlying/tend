package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func assertWorkEnumFailure(t *testing.T, op protocol.Operation, field, value string, allowed any) {
	t.Helper()
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	var details struct {
		Field         string   `json:"field"`
		Value         string   `json:"value"`
		AllowedValues []string `json:"allowed_values"`
	}
	if err := json.Unmarshal(op.Error.Details, &details); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(allowed)
	got, _ := json.Marshal(details.AllowedValues)
	if details.Field != field || details.Value != value || string(got) != string(want) {
		t.Fatalf("error details = %s, want %s=%q allowed %s", op.Error.Details, field, value, want)
	}
	if !strings.Contains(op.Error.Message, field) || !strings.Contains(op.Error.Message, value) || !strings.Contains(op.Error.Message, string(want)) {
		t.Fatalf("error cannot guide a corrected request: %s", op.Error.Message)
	}
}

func TestWorkItemPlanningGuessReplayPreservesCompleteAgreement(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Background = []protocol.SharedFact{{Kind: "goal", Text: "Owner needs design and a separate review before implementation", Source: "owner planning request"}}
	plan.Stages = []protocol.IssueStage{
		{StageID: "design", Title: "Design the approach", Status: "planned"},
		{StageID: "review", Title: "Review the complete design", Status: "planned", DependsOn: []string{"design"}},
	}
	original := h.createWork(plan)
	beforeEvents, beforeHistory := h.count("events"), h.count("work_item_revisions")

	// Reproduce the observed guesses without dropping context or the second
	// stage. Every rejected request must leave the persisted revision intact.
	for _, guess := range []struct {
		field, value string
	}{
		{"background[0].kind", "owner_request"},
		{"stages[1].status", "pending"},
		{"stages[1].status", "open"},
		{"stages[1].status", "todo"},
		{"stages[1].status", "ready"},
	} {
		t.Run(guess.value, func(t *testing.T) {
			plan := original.IssuePlan
			plan.Background = slices.Clone(plan.Background)
			plan.Stages = slices.Clone(plan.Stages)
			var allowed any = protocol.IssueStageStatusValues()
			if guess.field == "background[0].kind" {
				plan.Background[0].Kind = protocol.SharedFactKind(guess.value)
				allowed = protocol.SharedFactKindValues()
			} else {
				plan.Stages[1].Status = protocol.IssueStageStatus(guess.value)
			}
			env := h.env("work_items.update", workTarget(original.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: plan}, rev(original.Revision))
			op := h.exec(mainAgent, env, UpdateIssue{})
			assertWorkEnumFailure(t, op, guess.field, guess.value, allowed)
			// The deterministic failure is durable across same-ID retries.
			replay := h.exec(mainAgent, env, UpdateIssue{})
			if !reflect.DeepEqual(replay.Error, op.Error) {
				t.Fatalf("replayed error lost actionable details: %+v", replay.Error)
			}
			if stored := h.work(original.WorkItemID); !reflect.DeepEqual(stored, original) {
				t.Fatalf("invalid update altered the agreement: %+v", stored)
			}
			if h.count("events") != beforeEvents || h.count("work_item_revisions") != beforeHistory {
				t.Fatal("invalid update advanced events or revision history")
			}
		})
	}

	corrected := original.IssuePlan
	corrected.Background = slices.Clone(corrected.Background)
	corrected.Stages = slices.Clone(corrected.Stages)
	corrected.Background[0].Kind = protocol.SharedFactUserConstraint
	corrected.Stages[0].Status = protocol.WorkItemStageActive
	// A new request identity carries the corrected intent; one call succeeds.
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(original.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: corrected}, rev(original.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("corrected complete request failed: %+v", op)
	}
	stored := h.work(original.WorkItemID)
	if stored.Revision != original.Revision+1 || !reflect.DeepEqual(stored.IssuePlan, corrected) {
		t.Fatalf("corrected request lost context or review stage: %+v", stored)
	}
	if h.count("work_item_revisions") != beforeHistory+1 || h.count("events") != beforeEvents+1 {
		t.Fatal("correction should make exactly one ledger revision")
	}
}

func TestWorkItemNestedEvidenceErrorsRetainContainerPath(t *testing.T) {
	for _, location := range []string{"stage", "follow_on", "closure"} {
		t.Run(location, func(t *testing.T) {
			h := newHarness(t)
			plan := workPlan()
			plan.Stages = nil
			w := h.createWork(plan)
			badEvidence := []protocol.IssueEvidence{{Kind: "report", Reference: "report-ref", Summary: "Claimed result"}}
			var op protocol.Operation
			var field string
			switch location {
			case "stage":
				w.Stages = []protocol.IssueStage{{StageID: "design", Title: "Design", Status: "planned", Evidence: badEvidence}}
				field = "stages[0].evidence[0].kind"
			case "follow_on":
				w.FollowOns = []protocol.IssueFollowOn{{FollowOnID: "later", Kind: "suggestion", Text: "Optional follow-up", Source: "source", Status: "open", Evidence: badEvidence}}
				field = "follow_ons[0].evidence[0].kind"
			case "closure":
				field = "closure.evidence[0].kind"
			}
			if location == "closure" {
				op = h.exec(owner, h.env("work_items.close", workTarget(w.WorkItemID), protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "completed", Reason: "Finished", Evidence: badEvidence}}, rev(w.Revision)), CloseIssue{})
			} else {
				op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(w.Revision)), UpdateIssue{})
			}
			assertWorkEnumFailure(t, op, field, "report", protocol.IssueEvidenceKindValues())
		})
	}
}

func TestWorkItemDispositionEnumRetainsExistingIgnoredEntryBehavior(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "Design", Spec: testSpec})
	w := h.createWork(workPlan())
	link := h.exec(mainAgent, h.env("work_items.link_task", workTarget(w.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: task.TaskID, StageIDs: []string{"investigate"}}, rev(w.Revision)), LinkIssueTask{})
	if link.Status != protocol.OpSucceeded {
		t.Fatal(link)
	}
	w = h.work(w.WorkItemID)
	p := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}
	p.Intent = "Change investigation scope while honoring the original task agreement"
	p.TaskDispositions = []protocol.IssueTaskDisposition{{TaskID: task.TaskID, SpecVersion: task.SpecVersion, Action: "continue", Reason: "Preserve the original agreement"}}
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	assertWorkEnumFailure(t, op, "task_dispositions[0].action", "continue", protocol.IssueTaskDispositionActionValues())
	if stored := h.work(w.WorkItemID); !reflect.DeepEqual(stored, w) {
		t.Fatal("invalid task disposition changed the plan")
	}
	p.TaskDispositions[0].Action = protocol.WorkItemTaskContinueExisting
	// Historically unused extra dispositions are ignored; enum discovery does
	// not add new validation to unrelated entries in an existing valid request.
	p.TaskDispositions = append(p.TaskDispositions, protocol.IssueTaskDisposition{TaskID: "unused", Action: "ignored"})
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != task.SpecVersion {
		t.Fatalf("corrected disposition changed compatibility or task spec: %+v", op)
	}
}

func TestWorkItemUpdateExplicitEmptyStatusRemainsValid(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	p := protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan, Status: protocol.WorkItemUpdateSnoozed}
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	p.Status = ""
	env := h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision))
	var payload map[string]any
	_ = json.Unmarshal(env.Payload, &payload)
	payload["status"] = ""
	env.Payload, _ = json.Marshal(payload)
	op = h.exec(mainAgent, env, UpdateIssue{})
	if op.Status != protocol.OpSucceeded || h.work(w.WorkItemID).Status != protocol.WorkItemSnoozed || h.work(w.WorkItemID).Revision != w.Revision {
		t.Fatalf("explicit empty status no longer preserves current status: %+v", op)
	}
	p.Status = "closed"
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision)), UpdateIssue{})
	assertWorkEnumFailure(t, op, "status", "closed", protocol.WorkItemUpdateStatusValues())
}

func TestWorkItemMissingStageIDsExplainsAtomicCreationFailure(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	payload := createTaskPayload{Title: "Design", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision}
	for _, ids := range [][]string{nil, {}} {
		payload.StageIDs = ids
		op, _ := h.createTask(mainAgent, payload)
		expectWorkCode(t, op, protocol.CodeInvalidRequest)
		var details struct {
			Field      string   `json:"field"`
			Value      []string `json:"value"`
			MinItems   int      `json:"min_items"`
			WorkItemID string   `json:"work_item_id"`
		}
		if err := json.Unmarshal(op.Error.Details, &details); err != nil {
			t.Fatal(err)
		}
		if details.Field != "stage_ids" || len(details.Value) != 0 || details.MinItems != 1 || details.WorkItemID != w.WorkItemID || !strings.Contains(op.Error.Message, "at least one existing stage_id") {
			t.Fatalf("missing association guidance: %+v", op.Error)
		}
		if h.count("tasks") != 0 || h.count("task_specs") != 0 || !reflect.DeepEqual(h.work(w.WorkItemID), w) {
			t.Fatal("missing stage association partially created a task or revised its work item")
		}
	}
	payload.StageIDs = []string{"investigate"}
	op, _ := h.createTask(mainAgent, payload)
	if op.Status != protocol.OpSucceeded || h.count("tasks") != 1 {
		t.Fatalf("corrected stage association did not create exactly one task: %+v", op)
	}
}
