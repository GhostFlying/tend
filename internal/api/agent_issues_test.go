package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"testing"
)

func TestAgentIssueOwnerReadValidation(t *testing.T) {
	e := newEnv(t)
	path := "/api/v1/agent-issues?environment_id=hosted&local_id=main"
	for _, auth := range []string{"", e.retiredAgentToken(clientID)} {
		code := e.do("GET", path, auth, nil, nil)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("non-owner=%d", code)
		}
	}
	token := e.login(protocol.ScopeOwner)
	var out protocol.AgentIssuePage
	if code := e.do("GET", path, token, nil, &out); code != http.StatusOK || out.Items == nil || out.Coverage != "recorded_run_pins" || out.Identity.LocalID != "main" {
		t.Fatalf("empty=%d %+v", code, out)
	}
	for _, query := range []string{"", "?environment_id=hosted&local_id=main&limit=-1", "?environment_id=hosted&local_id=main&limit=101", "?environment_id=hosted&local_id=main&scope=running", "?environment_id=hosted&local_id=main&after=bad", "?environment_id=hosted&local_id=main&state=missing", "?environment_id=hosted&local_id=main&agent_revision=-1"} {
		if code := e.do("GET", "/api/v1/agent-issues"+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatalf("%s=%d", query, code)
		}
	}
}
