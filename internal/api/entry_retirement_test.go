package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetiredWorkItemOwnerRoutesCannotCreateOrMutateBusiness(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	// A historical App fixture proves retirement cannot merely depend on the
	// new ledger being empty; this is not a current public creation path.
	var old protocol.WorkItem
	raw := e.exec("work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Historical concern", Intent: "Retain history"}}, app.CreateIssue{})
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/work-items", "/api/v1/work-items/" + old.WorkItemID, "/api/v1/work-items/" + old.WorkItemID + "?revision=1", "/api/v1/work-items/" + old.WorkItemID + "/context", "/api/v1/work-items/" + old.WorkItemID + "/followups", "/api/v1/followups/old-rule/history"} {
		if status := e.do("GET", path, token, nil, nil); status != http.StatusNotFound {
			t.Errorf("retired route %s returned %d", path, status)
		}
	}
	for _, operation := range []string{"work_items.create", "work_items.update", "work_items.close", "work_items.reopen", "work_items.link_task", "work_items.followups_create", "work_items.followups_update", "work_items.followups_pause", "work_items.followups_resume", "work_items.followups_cancel", "work_items.followups_trigger", "work_items.refresh_delivery", "conversation.set_associations", "conversation.open_background"} {
		env := e.envelope(operation, protocol.Target{Type: "work_item", ID: old.WorkItemID}, &old.Revision, protocol.UpdateWorkItemPayload{IssuePlan: old.IssuePlan})
		if status, response := e.op(token, env); status != http.StatusBadRequest || response.Error == nil || response.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("retired operation %s still registered: %d %+v", operation, status, response)
		}
	}
}
