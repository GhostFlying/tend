package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCanonicalTaskDefinitionStageAdmissionUsesOnlyNewBindings(t *testing.T) {
	for _, kind := range []string{"new_active", "existing_active", "new_terminal"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			plan := workPlan()
			plan.Project = "default"
			issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, canonicalOwnerIssueCreate{Plan: plan}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
			p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Explicit task agreement", Spec: testSpec}
			if kind == "existing_active" {
				p.StageIDs = []string{"investigate"}
			}
			op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var created struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			if kind == "new_terminal" {
				task := h.task(created.TaskID)
				if op := h.exec(owner, h.env("tasks.cancel", taskTarget(task.TaskID), nil, rev(task.ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if !h.task(task.TaskID).Lifecycle.Terminal() {
					t.Fatal("owner cancellation did not terminate unstarted task")
				}
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			plan = issue.Plan
			plan.Stages[0].Status = protocol.WorkItemStageDone
			plan.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner confirms this lightweight step is settled"}}
			plan.Stages[0].TaskIDs = []string{created.TaskID}
			update := canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
			if kind != "new_terminal" {
				update.TaskDispositions = []protocol.IssueTaskDisposition{{TaskID: created.TaskID, SpecVersion: 1, Action: protocol.WorkItemTaskContinueExisting, Reason: "Continue the already frozen worker contract"}}
			}
			op = h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, update, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
			if kind == "new_active" {
				expectWorkCode(t, op, protocol.CodeInvalidTransition)
				current := canonicalTaskIssue(t, h, issue.IssueID)
				if current.Revision != issue.Revision || len(current.Plan.Stages[0].TaskIDs) != 0 {
					t.Fatal("rejected stage binding changed the issue")
				}
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var links int
					err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM work_item_task_links WHERE work_item_id=? AND task_id=?`, issue.IssueID, created.TaskID).Scan(&links)
					if links != 0 {
						t.Fatal("rejected binding leaked links")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}

func TestCanonicalTaskSecondaryEvidenceDoesNotBlockLegacyClosure(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		name := "legacy_responsibility"
		if canonical {
			name = "canonical_reference"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			var task string
			if canonical {
				issue := createCanonicalOwnerTestIssue(t, h)
				task = createCanonicalTaskForTest(t, h, issue)
			} else {
				op, created := h.createTask(owner, createTaskPayload{Title: "Legacy execution", Spec: testSpec})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				task = created.TaskID
			}
			legacy := h.createWork(workPlan())
			if op := h.exec(owner, h.env("work_items.link_task", workTarget(legacy.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: task, StageIDs: []string{"investigate"}}, rev(legacy.Revision)), LinkIssueTask{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			legacy = h.work(legacy.WorkItemID)
			plan := legacy.IssuePlan
			plan.Stages[0].Status = protocol.WorkItemStageDone
			plan.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner settled this separate concern"}}
			update := protocol.UpdateWorkItemPayload{IssuePlan: plan}
			if !canonical {
				update.TaskDispositions = []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 1, Action: protocol.WorkItemTaskContinueExisting, Reason: "Keep legacy shared responsibility"}}
			}
			if op := h.exec(owner, h.env("work_items.update", workTarget(legacy.WorkItemID), update, rev(legacy.Revision)), UpdateIssue{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			legacy = h.work(legacy.WorkItemID)
			closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "Owner finished this concern", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "The separate concern is complete"}}}}
			op := h.exec(owner, h.env("work_items.close", workTarget(legacy.WorkItemID), closure, rev(legacy.Revision)), CloseIssue{})
			if canonical {
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if h.work(legacy.WorkItemID).Status != "closed" || h.task(task).Lifecycle.Terminal() {
					t.Fatal("closing evidence-only item changed referenced execution")
				}
			} else {
				expectWorkCode(t, op, protocol.CodeInvalidTransition)
			}
		})
	}
}

func TestCanonicalTaskSecondaryEvidenceDoesNotCreateLegacyWaitCycle(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		name := "legacy_real_cycle"
		if canonical {
			name = "canonical_evidence_only"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			plan := workPlan()
			plan.Project = "default"
			plan.Stages = []protocol.IssueStage{{StageID: "first", Title: "First", Status: protocol.WorkItemStagePlanned}, {StageID: "second", Title: "Second", Status: protocol.WorkItemStagePlanned, DependsOn: []string{"first"}}}
			var tasks []string
			if canonical {
				issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, canonicalOwnerIssueCreate{Plan: plan}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
				for _, stage := range []string{"first", "second"} {
					issue = canonicalTaskIssue(t, h, issue.IssueID)
					p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Worker for " + stage, Spec: testSpec, StageIDs: []string{stage}}
					op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					var created struct {
						TaskID string `json:"task_id"`
					}
					if err := json.Unmarshal(op.Result, &created); err != nil {
						t.Fatal(err)
					}
					tasks = append(tasks, created.TaskID)
				}
			} else {
				primary := h.createWork(plan)
				for _, stage := range []string{"first", "second"} {
					primary = h.work(primary.WorkItemID)
					op, created := h.createTask(owner, createTaskPayload{Title: "Legacy worker for " + stage, Spec: testSpec, WorkItemID: primary.WorkItemID, WorkItemRevision: primary.Revision, StageIDs: []string{stage}})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					tasks = append(tasks, created.TaskID)
				}
			}
			evidence := h.createWork(plan)
			for i, stage := range []string{"first", "second"} {
				evidence = h.work(evidence.WorkItemID)
				op := h.exec(owner, h.env("work_items.link_task", workTarget(evidence.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: tasks[1-i], StageIDs: []string{stage}}, rev(evidence.Revision)), LinkIssueTask{})
				if i == 1 && !canonical {
					expectWorkCode(t, op, protocol.CodeInvalidRequest)
				} else if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if canonical {
				run, ok, _ := h.claim("linux-1")
				if !ok || run.TaskID != tasks[0] {
					t.Fatalf("secondary order changed primary scheduling: %+v %v", run, ok)
				}
			}
		})
	}
}
