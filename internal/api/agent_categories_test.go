package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAgentCategoryOwnerOperation(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	revision := int64(0)
	env := e.envelope("categories.put", protocol.Target{Type: "category", ID: "planner"}, &revision, protocol.PutAgentCategoryPayload{Title: "Planner", Description: "Clarify goals and next steps", State: "active"})
	status, res := e.op(token, env)
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("category create=%d %+v", status, res)
	}
	status, replay := e.op(token, env)
	if status != http.StatusOK || replay.Operation == nil || string(replay.Operation.Result) != string(res.Operation.Result) {
		t.Fatal("owner retry changed category", status, replay)
	}
}

func TestAgentCategoryReadUsesOwnerAuthorization(t *testing.T) {
	e := newEnv(t)
	for _, token := range []string{"", "invalid", e.retiredAgentToken(clientID)} {
		status := e.do("GET", "/api/v1/categories/planner", token, nil, nil)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("non-owner category read=%d", status)
		}
	}
	token := e.login(protocol.ScopeOwner)
	if status := e.do("GET", "/api/v1/categories/missing", token, nil, nil); status != http.StatusNotFound {
		t.Fatalf("missing category=%d", status)
	}
	revision := int64(0)
	env := e.envelope("categories.put", protocol.Target{Type: "category", ID: "planner"}, &revision, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"})
	if status, res := e.op(token, env); status != http.StatusOK || res.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, res)
	}
	var current protocol.AgentCategory
	if status := e.do("GET", "/api/v1/categories/planner", token, nil, &current); status != http.StatusOK || current.CategoryID != "planner" || current.Revision != 1 {
		t.Fatal(status, current)
	}
}
