package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOnlineRepositoryOwnerAPIAndReplay(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	zero := int64(0)
	envelope := e.envelope("repos.register", protocol.Target{Type: "repo", ID: "online"}, &zero, protocol.RegisterRepositoryPayload{RemoteURL: "https://example.com/online.git"})
	var first protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", token, envelope, &first); status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, first)
	}
	var replay protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", token, envelope, &replay); status != http.StatusOK || string(replay.Operation.Result) != string(first.Operation.Result) {
		t.Fatal("replay", status, replay)
	}
	var registered protocol.RepositoryRegistration
	if status := e.do("GET", "/api/v1/repos/online", token, nil, &registered); status != http.StatusOK || registered.ControlRevision != 1 || registered.RemoteURL != "https://example.com/online.git" {
		t.Fatal(status, registered)
	}
	for _, tok := range []string{"", e.login(protocol.ScopeAgent)} {
		if status := e.do("GET", "/api/v1/repos/online", tok, nil, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatal("non-owner catalog read", status)
		}
		if status := e.do("POST", "/api/v1/ops", tok, envelope, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatal("non-owner register", status)
		}
	}
}
