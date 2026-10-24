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
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: protocol.WorkItemPlan{Title: "Track investigation", Intent: "Understand and discuss next steps", Stages: []protocol.WorkItemStage{}}}, app.CreateWorkItem{})
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
	raw = e.exec(owner, "work_items.update", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &revision, protocol.UpdateWorkItemPayload{WorkItemPlan: item.WorkItemPlan, Status: "snoozed"}, app.UpdateWorkItem{})
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	confirm("work_item.updated", "snoozed")
	revision = item.Revision
	e.exec(owner, "work_items.close", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &revision, protocol.CloseWorkItemPayload{Closure: protocol.WorkItemClosure{Kind: "declined", Reason: "Owner ended this concern", Evidence: []protocol.WorkItemEvidence{{Kind: "owner_decision", Reference: "current_request", Summary: "Owner explicitly ends tracking"}}}}, app.CloseWorkItem{})
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
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: protocol.WorkItemPlan{Title: "Delivery", Intent: "Track review", Stages: []protocol.WorkItemStage{}}}, app.CreateWorkItem{})
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
