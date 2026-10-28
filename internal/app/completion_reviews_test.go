package app

import (
	"os"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestCompletionReviewsSurviveFailedUpdateAndAcknowledgeSharedWork(t *testing.T) {
	h := newHarness(t)
	_, created := h.createTask(owner, createTaskPayload{Title: "shared", Spec: testSpec})
	taskID := created.TaskID
	var items []protocol.WorkItem
	for range 2 {
		plan := workPlan()
		plan.Stages[0].Status = "active"
		item := h.createWork(plan)
		op := h.exec(owner, h.env("work_items.link_task", workTarget(item.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: taskID, StageIDs: []string{"investigate"}}, rev(item.Revision)), LinkIssueTask{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		items = append(items, h.work(item.WorkItemID))
	}
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(taskID).WorkspaceName), SubmitArtifact{})
	acc, _, _ := h.claim("linux-1")
	env := h.env(protocol.OpAcceptancesSubmit, runTarget(acc.RunID), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil)
	principal := runPrincipal(acc.RunID, acc.Role)
	if op := h.exec(principal, env, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.exec(principal, env, SubmitAcceptance{})
	if h.count("work_item_completion_reviews") != 3 {
		t.Fatal("completion must deduplicate per item")
	}
	read := func(id string) protocol.WorkItemView {
		t.Helper()
		var v protocol.WorkItemView
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.Issue(t.Context(), tx.Q(), id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := read(items[0].WorkItemID)
	if len(v.PendingReviews) != 1 || v.Stages[0].Status != "active" {
		t.Fatal(v)
	}
	pending := v.PendingReviews[0]
	detail := h.executionTask(taskID)
	if detail.Acceptances[0].AcceptanceID != pending.AcceptanceID {
		t.Fatal("acceptance cannot be referenced")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		art, err := views.Artifact(t.Context(), tx.Q(), detail.CurrentArtifactID)
		if err != nil {
			return err
		}
		if art.Acceptances[0].AcceptanceID != pending.AcceptanceID {
			t.Fatal("artifact view omitted acceptance ID")
		}
		messages, err := store.ListConversation(t.Context(), tx.Q(), 0, 0, 100)
		if err != nil {
			return err
		}
		var retained strings.Builder
		for _, m := range messages {
			retained.WriteString(m.Body)
		}
		if !strings.Contains(retained.String(), pending.AcceptanceID) {
			t.Fatal("notice omitted acceptance ID")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.UpdateWorkItemPayload{WorkItemPlan: v.WorkItemPlan, CompletionReviews: []protocol.CompletionReviewAcknowledgement{{EventID: pending.EventID, Reason: "Review complete; service responsibility continues"}}}
	p.Stages = append([]protocol.WorkItemStage(nil), p.Stages...)
	p.Stages[0].Status = "done"
	p.Stages[0].Evidence = []protocol.WorkItemEvidence{{Kind: "acceptance", Reference: detail.CurrentArtifactID, Summary: "wrong ID"}}
	op := h.exec(owner, h.env("work_items.update", workTarget(v.WorkItemID), p, rev(v.Revision)), UpdateIssue{})
	if op.Status != protocol.OpFailed || len(read(v.WorkItemID).PendingReviews) != 1 {
		t.Fatal("failed update lost pending review", op)
	}
	p.WorkItemPlan = v.WorkItemPlan
	op = h.exec(owner, h.env("work_items.update", workTarget(v.WorkItemID), p, rev(v.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := read(v.WorkItemID); len(got.PendingReviews) != 0 || got.Stages[0].Status != "active" || got.Status != "open" {
		t.Fatal(got)
	}
	other := read(items[1].WorkItemID)
	if len(other.PendingReviews) != 1 {
		t.Fatal("shared work acknowledgement leaked")
	}
	other.Stages[0].Status = "done"
	other.Stages[0].Evidence = []protocol.WorkItemEvidence{{Kind: "acceptance", Reference: pending.AcceptanceID, Summary: "Independently accepted"}}
	op = h.exec(owner, h.env("work_items.update", workTarget(other.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: other.WorkItemPlan}, rev(other.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded || len(read(other.WorkItemID).PendingReviews) != 0 || read(other.WorkItemID).Status != "open" {
		t.Fatal(op)
	}
}

func TestCompletionReviewBackfillAndLateLink(t *testing.T) {
	h := newHarness(t)
	taskID, acc := h.toAcceptanceClaim()
	if op := h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	plan := workPlan()
	plan.Stages[0].Status = "active"
	plan.Stages[0].TaskIDs = []string{taskID}
	plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "service", Title: "Retain service", Status: "active", TaskIDs: []string{taskID}})
	open := h.createWork(plan)
	plan.Stages[0].Status = "done"
	detail := h.executionTask(taskID)
	plan.Stages[0].Evidence = []protocol.WorkItemEvidence{{Kind: "acceptance", Reference: detail.Acceptances[0].AcceptanceID, Summary: "Accepted investigation"}}
	if op := h.exec(owner, h.env("work_items.update", workTarget(open.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: plan}, rev(open.Revision)), UpdateIssue{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// One task may satisfy delivery while a second linked stage retains service responsibility.
	plan.Stages = plan.Stages[:1]
	settled := h.createWork(plan)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), open.WorkItemID)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatal("late linkage did not retain completed execution for review")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../store/migrations/0037_work_item_completion_reviews.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `DROP TABLE work_item_completion_reviews`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), string(migration))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pending, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), open.WorkItemID)
		if err != nil {
			return err
		}
		if len(pending) != 1 || pending[0].AcceptanceID != detail.Acceptances[0].AcceptanceID {
			t.Fatal(pending)
		}
		done, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), settled.WorkItemID)
		if err != nil {
			return err
		}
		if len(done) != 0 {
			t.Fatal("settled stages reopened", done)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
