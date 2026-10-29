package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
)

func TestPlannerMainRatingChangesActualQueueWithoutChangingContract(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	first := createCanonicalTaskForTest(t, h, issue)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	second := createCanonicalTaskForTest(t, h, issue)
	before := h.storedSpec(second, 1)
	_, delivery := h.say("Prioritize the second task without changing its technical plan")
	turn := startHostedReview(t, h, "main", delivery)
	task := h.task(second)
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "task", ID: second}, rev(task.ControlRevision), protocol.RateTaskPayload{Rating: protocol.RatingP0}, RateTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	denv := h.env("actions.create", protocol.Target{Type: "task", ID: second}, protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}, rev(h.task(second).ControlRevision))
	_, denied := h.app.Execute(t.Context(), mainAgent, denv, HostedCoordinationCommand{Command: CreateAction{}, ExecutionID: turn.RequestID, EffectSeq: 2})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal("planner gained task execution planning", denied)
	}
	after := h.storedSpec(second, 1)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("rating changed frozen task contract")
	}
	claimed, ok, _ := h.claim("linux-1")
	if !ok || claimed.TaskID != second || claimed.TaskID == first {
		t.Fatal("display order did not reach scheduler", claimed, ok)
	}
}
func TestPlannerUserOrderIsIndependentOfTaskPriority(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	put := func(title, kind string, rank int64) protocol.PlannerEntry {
		t.Helper()
		entry := protocol.PlannerEntry{Kind: kind, IssueID: issue.IssueID, Title: title, Responsible: owner.Subject, Rank: rank, Status: "open", Reason: "Owner chooses order"}
		op := h.exec(owner, h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, protocol.PutPlannerEntryPayload{Entry: entry}, nil), PutPlannerEntry{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var v protocol.PlannerEntry
		if err := json.Unmarshal(op.Result, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	put("Later human action", "user_action", 20)
	first := put("First human action", "user_action", 10)
	put("First Issue to coordinate", "issue_priority", 1)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		p, err := views.NextActions(t.Context(), tx.Q(), "user_action", "")
		if err == nil && (len(p.Items) != 2 || p.Items[0].EntryID != first.EntryID) {
			t.Fatal(p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("tasks") != 0 {
		t.Fatal("personal planning created execution work")
	}
}

func TestPlannerManagerProposesOnlyLocalUnorderedActions(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	other := createCanonicalOwnerTestIssue(t, h)
	turn, principal := startHostedIssueReview(t, h, issue)
	p := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, Title: "Owner reviews the investigation result", Responsible: owner.Subject, Status: "open", Reason: "Local candidate; main will order personal work"}}
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "planner_entry"}, nil, p, PutPlannerEntry{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p.Entry.Rank = 1
	denv := h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, p, nil)
	if _, err := h.app.Execute(t.Context(), principal, denv, HostedCoordinationCommand{Command: PutPlannerEntry{}, ExecutionID: turn.RequestID, EffectSeq: 2}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("manager changed global personal order", err)
	}
	p.Entry.Rank = 0
	p.Entry.IssueID = other.IssueID
	env := h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, p, nil)
	_, err := h.app.Execute(t.Context(), principal, env, HostedCoordinationCommand{Command: PutPlannerEntry{}, ExecutionID: turn.RequestID, EffectSeq: 3})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("manager proposed a sibling's action", err)
	}
}
