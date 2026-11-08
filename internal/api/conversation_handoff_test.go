package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestConversationHandoffDisabledWithoutHostedMain(t *testing.T) {
	e := newEnv(t)
	owner := e.login(protocol.ScopeOwner)
	var view protocol.ConversationHandoffView
	if status := e.do("GET", "/api/v1/conversation/handoff", owner, nil, &view); status != http.StatusOK || view.Enabled {
		t.Fatalf("disabled policy: %+v HTTP%d", view, status)
	}
	var response protocol.APIResponse
	env := e.envelope("conversation.handoff", protocol.Target{Type: "conversation"}, nil, protocol.ConversationHandoffPayload{})
	if status := e.do("POST", "/api/v1/ops", owner, env, &response); status != http.StatusBadRequest {
		t.Fatalf("disabled handoff accepted: %+v HTTP%d", response, status)
	}
}

func TestConversationHandoffSettingsAPIRange(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	owner := e.login(protocol.ScopeOwner)
	env := e.envelope("conversation.set_handoff_settings", protocol.Target{Type: "conversation"}, nil, protocol.SetConversationHandoffSettingsPayload{IdleMinutes: 10081})
	revision := int64(0)
	env.ExpectedControlRevision = &revision
	var response protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", owner, env, &response); status != http.StatusOK || response.Operation == nil || response.Operation.Error == nil || response.Operation.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("invalid range response: %+v HTTP%d", response, status)
	}
}

func TestConversationHandoffReturnsDurableMaintenanceAndPreparing(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	e.server.cfg.ConversationHandoff = func(ctx context.Context) (protocol.ConversationHandoffView, error) {
		maintenance, err := e.app.ConversationMaintenance(ctx, "main")
		pending := maintenance != nil && (maintenance.Status == "pending" || maintenance.Status == "sending")
		return protocol.ConversationHandoffView{Enabled: true, Pending: pending, Preparing: pending, Maintenance: maintenance}, err
	}
	owner := e.login(protocol.ScopeOwner)
	env := e.envelope("conversation.handoff", protocol.Target{Type: "conversation"}, nil, protocol.ConversationHandoffPayload{})
	var response protocol.APIResponse
	if code := e.do("POST", "/api/v1/ops", owner, env, &response); code != http.StatusOK {
		t.Fatal(code)
	}
	var view protocol.ConversationHandoffView
	if code := e.do("GET", "/api/v1/conversation/handoff", owner, nil, &view); code != http.StatusOK || !view.Pending || !view.Preparing || view.Maintenance == nil || view.Maintenance.RequestID != env.RequestID {
		t.Fatalf("queued maintenance HTTP%d %+v", code, view)
	}
	d, _, ok, err := e.app.NextDelivery(context.Background(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if err := e.app.FinishDelivery(context.Background(), d, app.DeliveryResult{Outcome: app.Failed, Reason: "summary requires an idle conversation"}); err != nil {
		t.Fatal(err)
	}
	if code := e.do("GET", "/api/v1/conversation/handoff", owner, nil, &view); code != http.StatusOK || view.Pending || view.Preparing || view.Maintenance == nil || view.Maintenance.Status != "failed" || view.Maintenance.Reason != "summary requires an idle conversation" {
		t.Fatalf("failed maintenance HTTP%d %+v", code, view)
	}
}
