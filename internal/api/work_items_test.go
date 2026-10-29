package api

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
)

// The old public facade is retired. Its immutable ledger representation still
// has a dedicated historical reader; this test does not re-register HTTP routes.
func TestHistoricalWorkItemViewsRetainRevisions(t *testing.T) {
	e := newEnv(t)
	e.configure()
	var old protocol.WorkItem
	raw := e.exec("work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Historical concern", Intent: "Preserve original intent"}}, app.CreateIssue{})
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	plan := old.IssuePlan
	plan.Title = "Later historical revision"
	e.exec("work_items.update", protocol.Target{Type: "work_item", ID: old.WorkItemID}, &old.Revision, protocol.UpdateWorkItemPayload{IssuePlan: plan}, app.UpdateIssue{})
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		prior, err := store.GetIssueRevision(t.Context(), tx.Q(), old.WorkItemID, 1)
		if err != nil {
			return err
		}
		current, err := views.Issue(t.Context(), tx.Q(), old.WorkItemID)
		if err != nil {
			return err
		}
		if prior.Title != "Historical concern" || current.Title != "Later historical revision" || current.Revision != 2 {
			t.Fatal(prior, current)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
