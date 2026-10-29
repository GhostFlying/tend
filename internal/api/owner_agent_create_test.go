package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOwnerAPIImmediatelyCreatesSelectedRegisteredManager(t *testing.T) {
	e := newEnv(t)
	e.configure()
	principal := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
	zero := int64(0)
	put := e.envelope("categories.put", protocol.Target{Type: "category", ID: "planner"}, &zero, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"})
	if op, err := e.app.Execute(t.Context(), principal, &put, app.PutAgentCategory{}); err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "manager", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "HOSTED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "manager", Revision: 1, DisplayName: "Manager", RuntimeID: "runtime", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "model", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	admit := e.envelope("hosted.configure", protocol.Target{Type: "hosted_configuration"}, &zero, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
	if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &admit, app.AcceptHostedConfiguration{Prepared: prepared}); err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	e.app = app.New(e.store, e.ids, e.clock.Now, app.WithHostedConfiguration(prepared))
	e.server.app = e.app
	token := e.login(protocol.ScopeOwner)
	assignee := protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/manager"}
	env := e.envelope("issues.create", protocol.Target{Type: "issue"}, nil, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "New assigned Issue", Project: "default", Intent: "Investigate the actual owner goal"}, Assignee: &assignee})
	var response protocol.APIResponse
	if status := e.do(http.MethodPost, "/api/v1/ops", token, env, &response); status != http.StatusOK || response.Operation == nil || response.Operation.Error != nil {
		t.Fatal(status, response)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(response.Operation.Result, &issue); err != nil || issue.Assignee != assignee {
		t.Fatal(issue, err)
	}
	if status := e.do(http.MethodPost, "/api/v1/ops", token, env, &response); status != http.StatusOK || response.Operation == nil || response.Operation.Error != nil {
		t.Fatal(status, response)
	}
	var replay protocol.Issue
	if err := json.Unmarshal(response.Operation.Result, &replay); err != nil || replay.IssueID != issue.IssueID {
		t.Fatal(replay, err)
	}
	if _, _, ok, err := e.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID); err != nil || !ok {
		t.Fatal("registered manager was not durably woken", ok, err)
	}
	if got := e.count(`SELECT COUNT(*) FROM issue_controls`); got != 1 {
		t.Fatal("API replay duplicated Issue", got)
	}
}
