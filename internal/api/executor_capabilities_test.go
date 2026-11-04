package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityDefinitionNamedCLIAndHTTPHistoryRecovery(t *testing.T) {
	e := newEnv(t)
	c := newCapabilityCLI(t, e)
	payload := `{"title":"Paint","description":"Exact tool evidence","state":"active","contract_version":1,"verification_policy":"tool_inventory_v1","tool_name":"paint_tool"}`
	original := c.run("capability", "put", "paint", "--revision", "0", "--data", payload, "--dry-run")
	c.run("request", "replay", "--data", string(original))
	c.run("request", "replay", "--data", string(original))
	var current protocol.CapabilityDefinition
	if err := json.Unmarshal(c.run("capability", "show", "paint", "--definition-revision", "1", "--json"), &current); err != nil || current.Revision != 1 || current.ToolName != "paint_tool" {
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
