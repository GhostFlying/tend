package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityDefinitionNamedCLIAndHTTPHistoryRecovery(t *testing.T) {
	e := newEnv(t)
	c := newCapabilityCLI(t, e)
	payload := `{"title":"Paint","description":"Explicit configuration label","state":"active","contract_version":1,"verification_policy":"declaration_v1"}`
	original := c.run("capability", "put", "paint", "--revision", "0", "--data", payload, "--dry-run")
	c.run("request", "replay", "--data", string(original))
	c.run("request", "replay", "--data", string(original))
	var current protocol.CapabilityDefinition
	if err := json.Unmarshal(c.run("capability", "show", "paint", "--definition-revision", "1", "--json"), &current); err != nil || current.Revision != 1 || current.VerificationPolicy != "declaration_v1" || current.ToolName != "" {
		t.Fatal(current, err)
	}
	var page protocol.CapabilityPage
	if err := json.Unmarshal(c.run("capability", "list", "--search", "paint", "--state", "all", "--limit", "1", "--json"), &page); err != nil || len(page.Items) != 1 || page.Items[0].CapabilityID != "paint" {
		t.Fatal(page, err)
	}
	c.run("capability", "put", "paint", "--revision", "1", "--data", payload)
	if err := json.Unmarshal(c.run("capability", "history", "paint", "--limit", "1", "--json"), &page); err != nil || page.Next == "" {
		t.Fatal(page, err)
	}
	if code, _ := c.call("capability", "history", "paint", "--state", "retired", "--after", page.Next); code == 0 {
		t.Fatal("changed cursor filters accepted")
	}
	if err := json.Unmarshal(c.run("capability", "history", "paint", "--limit", "1", "--after", page.Next, "--json"), &page); err != nil || len(page.Items) != 1 || page.Items[0].Revision != 2 {
		t.Fatal(page, err)
	}
	token := e.login(protocol.ScopeOwner)
	for _, path := range []string{"/api/v1/capabilities?state=confirmed", "/api/v1/capabilities?limit=0", "/api/v1/capabilities?extra=1", "/api/v1/capabilities?state=active&state=all", "/api/v1/capabilities/paint?revision=0", "/api/v1/agent-capabilities?agent=paint", "/api/v1/agent-capabilities?state=confirmed"} {
		if status := e.do("GET", path, token, nil, nil); status != http.StatusBadRequest {
			t.Fatal(path, status)
		}
	}
	if status := e.do("GET", "/api/v1/capabilities", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal("unauthenticated catalog", status)
	}
	for _, args := range [][]string{{"capability", "list", "--help"}, {"capability", "show", "--help"}, {"capability", "history", "--help"}, {"capability", "put", "--help"}, {"agents", "capabilities", "list", "--help"}, {"agents", "capabilities", "show", "--help"}, {"agents", "capabilities", "history", "--help"}, {"agents", "capabilities", "evidence", "--help"}, {"agents", "local", "capabilities", "verify", "--help"}, {"agents", "local", "capabilities", "status", "--help"}, {"agents", "local", "capabilities", "reconcile", "--help"}, {"task", "capability-explain", "--help"}} {
		c.run(args...)
	}
}

func TestCapabilityDeclarationRevisionCLIAndHTTPPreserveHistory(t *testing.T) {
	e := newEnv(t)
	e.configure()
	c := newCapabilityCLI(t, e)
	token := e.login(protocol.ScopeOwner)
	zero := int64(0)
	code, op := e.op(token, e.envelope("categories.put", protocol.Target{Type: "category", ID: "worker"}, &zero, protocol.PutAgentCategoryPayload{Title: "Worker", State: "active"}))
	if code != http.StatusOK || op.Operation.Status != protocol.OpSucceeded {
		t.Fatal(code, op)
	}
	cfg := agentconfig.Config{FormatVersion: 1, CapabilityVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.OrcaWorker, CLI: "codex", ExecutableRef: "orca", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "cedar", Revision: 1, DisplayName: "Cedar", RuntimeID: "runtime", Categories: []string{"worker"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "model", Enabled: true}}}
	runtime := &protocol.RunnerRuntime{InstanceID: "declarations", Generation: 1, Sequence: 1, Protocol: 2, ProtocolMin: 1, ProtocolMax: 2, JournalVersion: 17, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityAgentInventory, protocol.CapabilityAgentExecution, protocol.CapabilityExecutorDeclarations}}
	execute := func(operation string, revision *int64, payload any, command app.Command) {
		t.Helper()
		env := e.envelope(operation, protocol.Target{Type: "host", ID: "host-a"}, revision, payload)
		result, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}, &env, command)
		if err != nil || result.Status != protocol.OpSucceeded {
			t.Fatal(result, err)
		}
	}
	execute(protocol.OpRunnerHello, nil, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}, app.Hello{})
	register := func(revision int64) {
		t.Helper()
		definition, err := agentconfig.Validate("host-a", cfg, agentconfig.SourceReference{Kind: agentconfig.Inline, Reference: "local-definition"})
		if err != nil {
			t.Fatal(err)
		}
		_, hash, _ := definition.CanonicalPublic()
		expected := revision - 1
		execute(protocol.OpRunnerInventoryRegister, &expected, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: revision, SnapshotID: fmt.Sprintf("declaration-%d", revision), PublicSHA256: hash, Projection: definition.Public()}, app.RegisterAgentInventory{})
	}
	register(1)
	cfg.Agents[0].Revision = 2
	cfg.Agents[0].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	register(2)
	var page protocol.AgentCapabilityRevisionPage
	if err := json.Unmarshal(c.run("agents", "capabilities", "revisions", "host-a", "cedar", "--limit", "1"), &page); err != nil || len(page.Items) != 1 || page.Next == "" || page.Items[0].Definition.Revision != 1 || len(page.Items[0].Definition.Capabilities) != 0 {
		t.Fatal(page, err)
	}
	cursor := page.Next
	page = protocol.AgentCapabilityRevisionPage{}
	cfg.Agents[0].Revision = 3
	cfg.Agents[0].Capabilities = nil
	register(3)
	if err := json.Unmarshal(c.run("agents", "capabilities", "revisions", "host-a", "cedar", "--limit", "1", "--after", cursor), &page); err != nil || len(page.Items) != 1 || page.Items[0].Definition.Revision != 2 || len(page.Items[0].Definition.Capabilities) != 1 || page.Next != "" {
		t.Fatal("history watermark drifted", page, err)
	}
	if code, _ := c.call("agents", "capabilities", "revisions", "host-a", "cedar", "--agent-revision", "3", "--after", cursor); code == 0 {
		t.Fatal("revision cursor filter changed")
	}
	page = protocol.AgentCapabilityRevisionPage{}
	if err := json.Unmarshal(c.run("agents", "capabilities", "revisions", "host-a", "cedar", "--agent-revision", "3"), &page); err != nil || len(page.Items) != 1 || len(page.Items[0].Definition.Capabilities) != 0 || page.Items[0].SnapshotID != "declaration-3" {
		t.Fatal(page, err)
	}
	c.run("agents", "capabilities", "revisions", "--help")
	for _, auth := range []string{"", e.retiredAgentToken(clientID)} {
		code := e.do("GET", "/api/v1/agent-capabilities/host-a/cedar/revisions", auth, nil, nil)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatal("non-owner declaration history", code)
		}
	}
	for _, query := range []string{"?agent_revision=0", "?agent_revision=2&agent_revision=3", "?limit=0", "?unknown=1"} {
		if code := e.do("GET", "/api/v1/agent-capabilities/host-a/cedar/revisions"+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal(query, code)
		}
	}
}
