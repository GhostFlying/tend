package contextbuilder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWorkItemChangesReachIncrementalContext(t *testing.T) {
	e := newEnv(t)
	if err := e.b.Confirm(t.Context(), e.prepare(0).ID); err != nil {
		t.Fatal(err)
	}
	var item protocol.WorkItem
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Track investigation", Intent: "Understand and discuss next steps", Stages: []protocol.IssueStage{}}}, app.CreateIssue{})
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	confirm := func(kind, content string) {
		t.Helper()
		p := e.prepare(0)
		if p == nil || p.Full || !strings.Contains(p.Text, kind) || !strings.Contains(p.Text, content) {
			t.Fatalf("missing %s/%s delta: %+v", kind, content, p)
		}
		if err := e.b.Confirm(t.Context(), p.ID); err != nil {
			t.Fatal(err)
		}
	}
	confirm("work_item.created", item.WorkItemID)
	revision := item.Revision
	raw = e.exec(owner, "work_items.update", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &revision, protocol.UpdateWorkItemPayload{IssuePlan: item.IssuePlan, Status: "snoozed"}, app.UpdateIssue{})
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	confirm("work_item.updated", "snoozed")
	revision = item.Revision
	e.exec(owner, "work_items.close", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &revision, protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "declined", Reason: "Owner ended this concern", Evidence: []protocol.IssueEvidence{{Kind: "owner_decision", Reference: "current_request", Summary: "Owner explicitly ends tracking"}}}}, app.CloseIssue{})
	// Rebuilding after compaction must preserve an unseen closed item even
	// though it has left the open work-item snapshot.
	if err := e.b.Invalidate(t.Context(), thread); err != nil {
		t.Fatal(err)
	}
	full := e.prepare(0)
	if full == nil || !full.Full || !strings.Contains(full.Text, "work_item.closed") || !strings.Contains(full.Text, item.WorkItemID) {
		t.Fatalf("lost unseen closure: %+v", full)
	}
}

func TestDeliveryFailureAppearsInContextAttention(t *testing.T) {
	e := newEnv(t)
	var item protocol.WorkItem
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Delivery", Intent: "Track review", Stages: []protocol.IssueStage{}}}, app.CreateIssue{})
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	if err := e.b.Confirm(t.Context(), e.prepare(0).ID); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: e.ids.New(protocol.PrefixEvent), ObjectType: "work_item", ObjectID: item.WorkItemID, ObjectVersion: item.Revision, Kind: "work_item.delivery_changed", BodyJSON: `{"error_kind":"authentication_required","state":"open"}`})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := e.prepare(0)
	if p == nil || p.Full || !strings.Contains(p.Text, "Needs attention") || !strings.Contains(p.Text, "authentication_required") {
		t.Fatalf("missing failure attention: %+v", p)
	}
}

func TestCanonicalIssueContextUsesActualDefinitionAndTools(t *testing.T) {
	e := newEnv(t)
	var issue protocol.Issue
	raw := e.exec(owner, "issues.create", protocol.Target{Type: "issue"}, nil, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Current responsibility", Intent: "Investigate a vague goal", Scope: "Read-only report", CompletionCriteria: []string{"Explain uncertainty"}}}, app.CanonicalOwnerIssueCommand{Action: "create"})
	if err := json.Unmarshal(raw, &issue); err != nil {
		t.Fatal(err)
	}
	full := e.prepare(0)
	if full == nil || !strings.Contains(full.Text, "Open Issues and responsibility") || !strings.Contains(full.Text, "scope: Read-only report") || !strings.Contains(full.Text, "Explain uncertainty") || !strings.Contains(full.Text, "issues_context_read") || strings.Contains(full.Text, "work_items_list") {
		t.Fatalf("canonical snapshot used legacy projection: %+v", full)
	}
	if err := e.b.Confirm(t.Context(), full.ID); err != nil {
		t.Fatal(err)
	}
	e.exec(owner, "issues.pause", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Pause new dispatch"}, app.CanonicalOwnerIssueCommand{Action: "pause"})
	delta := e.prepare(0)
	if delta == nil || !strings.Contains(delta.Text, "state paused") || !strings.Contains(delta.Text, issue.IssueID) || strings.Contains(delta.Text, "work_items_read") {
		t.Fatalf("canonical delta lost actual state: %+v", delta)
	}
}
