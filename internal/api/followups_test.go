package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFollowupOwnerAPIReadWriteAndCapability(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	var w protocol.WorkItem
	if err := json.Unmarshal(e.exec("work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: protocol.WorkItemPlan{Title: "Existing work", Intent: "Implement agreed change"}}, app.CreateWorkItem{}), &w); err != nil {
		t.Fatal(err)
	}
	p := protocol.FollowupSpec{Purpose: "Check result", Instruction: "Read current facts", Kind: "at", At: e.clock.Now().Add(time.Hour).UnixMilli(), Timezone: "UTC"}
	env := e.envelope("work_items.followups_create", protocol.Target{Type: "work_item", ID: w.WorkItemID}, &w.Revision, p)
	_, res := e.op(token, env)
	if res.Error == nil && (res.Operation == nil || res.Operation.Error == nil) {
		t.Fatal("disabled hosted mode accepted followup")
	}
	app.WithFollowups(true)(e.app)
	e.server.cfg.HostedMainAgent = true
	env = e.envelope("work_items.followups_create", protocol.Target{Type: "work_item", ID: w.WorkItemID}, &w.Revision, p)
	status, res := e.op(token, env)
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, res)
	}
	var rule protocol.FollowupRule
	if err := json.Unmarshal(res.Operation.Result, &rule); err != nil {
		t.Fatal(err)
	}
	_, again := e.op(token, env)
	if string(again.Operation.Result) != string(res.Operation.Result) {
		t.Fatal("request replay changed rule")
	}
	path := "/api/v1/work-items/" + w.WorkItemID + "/followups"
	if status := e.do("GET", path, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	var page protocol.FollowupPage
	if status := e.do("GET", path, token, nil, &page); status != http.StatusOK || len(page.Items) != 1 || !page.Enabled {
		t.Fatal(status, page)
	}
	var history protocol.FollowupHistoryPage
	if status := e.do("GET", "/api/v1/followups/"+rule.RuleID+"/history", token, nil, &history); status != http.StatusOK || len(history.Revisions) != 1 {
		t.Fatal(status, history)
	}
	pause := e.envelope("work_items.followups_pause", protocol.Target{Type: "followup", ID: rule.RuleID}, &rule.Revision, protocol.UpdateFollowupPayload{})
	pause.Reason = "owner paused"
	_, paused := e.op(token, pause)
	if paused.Operation == nil || paused.Operation.Status != protocol.OpSucceeded {
		t.Fatal(paused)
	}
	stale := e.envelope("work_items.followups_resume", pause.Target, &rule.Revision, protocol.UpdateFollowupPayload{})
	stale.Reason = "owner resumed"
	_, rejected := e.op(token, stale)
	if rejected.Operation == nil || rejected.Operation.Error == nil || rejected.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(rejected)
	}
}
