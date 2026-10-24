package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkItemOwnerReadsHistoryAndStaleUpdate(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	plan := protocol.WorkItemPlan{Title: "Investigate", Intent: "Understand the issue", Stages: []protocol.WorkItemStage{{StageID: "research", Title: "Research", Status: "planned"}}}
	var item protocol.WorkItem
	if err := json.Unmarshal(e.exec("work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: plan}, app.CreateWorkItem{}), &item); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/work-items/" + item.WorkItemID
	if st := e.do("GET", path, "", nil, nil); st != http.StatusUnauthorized {
		t.Fatal(st)
	}
	agent := e.loginAs("agent-client", protocol.ScopeAgent)
	if st := e.do("GET", path, agent, nil, nil); st != http.StatusForbidden {
		t.Fatal(st)
	}
	var page protocol.WorkItemPage
	if st := e.do("GET", "/api/v1/work-items", token, nil, &page); st != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("list = %d %+v", st, page)
	}
	plan = item.WorkItemPlan
	plan.Title = "Investigate updated goal"
	revision := item.Revision
	update := e.envelope("work_items.update", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &revision, protocol.UpdateWorkItemPayload{WorkItemPlan: plan})
	if st, res := e.op(token, update); st != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("update = %d %+v", st, res)
	}
	stale := e.envelope("work_items.update", update.Target, &revision, protocol.UpdateWorkItemPayload{WorkItemPlan: plan})
	_, res := e.op(token, stale)
	if res.Operation == nil || res.Operation.Error == nil || res.Operation.Error.Code != protocol.CodeStaleRevision || res.CurrentWorkItem == nil || res.CurrentWorkItem.Revision != 2 {
		t.Fatalf("stale = %+v", res)
	}
	var historical protocol.WorkItemView
	if st := e.do("GET", path+"?revision=1", token, nil, &historical); st != http.StatusOK || historical.Title != "Investigate" || historical.Column != "historical" || len(historical.Tasks) != 0 || len(historical.Observations) != 0 {
		t.Fatalf("history = %d %+v", st, historical)
	}
	for _, suffix := range []string{"?revision=0", "?revision=oops"} {
		if st := e.do("GET", path+suffix, token, nil, nil); st != http.StatusBadRequest {
			t.Fatalf("invalid revision = %d", st)
		}
	}
	if st := e.do("GET", path+"?revision=99", token, nil, nil); st != http.StatusNotFound {
		t.Fatal(st)
	}
}
