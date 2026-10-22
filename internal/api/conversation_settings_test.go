package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestConversationSettingsOwnerAPIAndReplayWithoutCatalog(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	models := []protocol.ConversationModel{{ID: "a", Label: "Model A", DefaultEffort: "medium", SupportedEfforts: []string{"medium", "high"}}}
	var available atomic.Bool
	available.Store(true)
	e.server.cfg.ConversationSettings = func(ctx context.Context) (protocol.ConversationSettingsView, error) {
		stored, err := e.app.ConversationSettings(ctx)
		return protocol.ConversationSettingsView{Enabled: true, Available: available.Load(), Revision: stored.Revision,
			Model: stored.Model, Effort: stored.Effort, Models: models}, err
	}
	owner := e.login(protocol.ScopeOwner)
	var view protocol.ConversationSettingsView
	if status := e.do("GET", "/api/v1/conversation/settings", "", nil, &view); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settings status = %d", status)
	}
	if status := e.do("GET", "/api/v1/conversation/settings", owner, nil, &view); status != http.StatusOK || !view.Available || view.Revision != 0 {
		t.Fatalf("initial settings = %+v (HTTP %d)", view, status)
	}
	env := e.envelope("conversation.set_settings", protocol.Target{Type: "conversation"}, nil, protocol.SetConversationSettingsPayload{Model: "a", Effort: "high"})
	revision := int64(0)
	env.ExpectedControlRevision = &revision
	var response protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", owner, env, &response); status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("set = %+v (HTTP %d)", response, status)
	}
	// The receipt is durable even when capability validation is unavailable.
	available.Store(false)
	if status := e.do("POST", "/api/v1/ops", owner, env, &response); status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("replay = %+v (HTTP %d)", response, status)
	}
	if status := e.do("GET", "/api/v1/conversation/settings", owner, nil, &view); status != http.StatusOK || view.Revision != 1 || view.Model != "a" || view.Effort != "high" || view.Available {
		t.Fatalf("saved settings = %+v (HTTP %d)", view, status)
	}
}

func TestConversationSettingsDisabledWithoutHostedMain(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	var view protocol.ConversationSettingsView
	if status := e.do("GET", "/api/v1/conversation/settings", token, nil, &view); status != http.StatusOK || view.Enabled || view.Available {
		t.Fatalf("disabled settings = %+v (HTTP %d)", view, status)
	}
}
