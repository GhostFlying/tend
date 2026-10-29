package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHistoricalBackgroundOwnerSourcesAndMetrics(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	app.WithFollowups(true)(e.app)
	e.server.cfg.HostedMainAgent = true
	var w protocol.WorkItem
	if err := json.Unmarshal(e.exec("work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Check", Intent: "Agreed scope"}}, app.CreateIssue{}), &w); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// Historical ledger setup is explicit App use; the old owner command
		// is not registered by this test or by the current service.
		env := e.envelope("work_items.followups_create", protocol.Target{Type: "work_item", ID: w.WorkItemID}, &w.Revision, protocol.FollowupSpec{Purpose: "Check", Instruction: "Read facts", Kind: "at", At: e.clock.Now().UnixMilli(), Timezone: "UTC"})
		op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, &env, app.FollowupCommand{Action: "create"})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal("historical owner setup", op, err)
		}
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_item_contexts SET baseline_revision=revision,coverage='fixture initialized' WHERE work_item_id=?`, w.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, _, ok, err := e.app.NextDelivery(t.Context(), "background_thread:work-item:"+w.WorkItemID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	var p app.HandleMessage
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/background/" + p.MessageID + "/sources"
	var sources protocol.BackgroundSourcePage
	if status := e.do("GET", path, token, nil, &sources); status != http.StatusOK || len(sources.Sources) != 2 {
		t.Fatal(status, sources)
	}
	if status := e.do("GET", path+"?after=-1", token, nil, nil); status != http.StatusBadRequest {
		t.Fatal(status)
	}
	if status := e.do("GET", path, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	var metrics protocol.CoordinationMetrics
	if status := e.do("GET", "/api/v1/coordination/metrics", token, nil, &metrics); status != http.StatusOK || metrics.CoalescedSources != 1 {
		t.Fatal(status, metrics)
	}
}
