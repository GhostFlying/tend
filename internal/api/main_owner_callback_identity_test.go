package api

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/tools"
	"testing"
)

func TestMainOwnerRecoveryRetainsOneNativeCallIdentity(t *testing.T) {
	e, call := mainOwnerFixture(t)
	service := tools.New(e.app, e.store, e.context, quiet())
	service.SetOwnerTools(e.server.MainOwnerTools())
	invoke := func(seq int64) {
		payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Original", Project: "default", Intent: "Report", Stages: []protocol.IssueStage{}}})
		env := protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "issues.create", Target: protocol.Target{Type: "issue"}, Payload: payload}
		args, _ := json.Marshal(map[string]any{"name": ownerToolName("issues.create"), "arguments": protocol.OwnerToolArguments{Target: env.Target, Payload: payload, EffectSeq: seq}})
		result := service.Handle(t.Context(), tools.Call{Principal: call.Actor, ExecutionRequestID: call.ExecutionID, TurnID: "turn", Native: "same-native-id", Tool: "owner_call", Arguments: args})
		t.Logf("seq %d success %t", seq, result.Success)
	}
	invoke(1)
	invoke(2)
	if n := e.count(`SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Errorf("one native callback created %d Issues", n)
	}
}
func TestMainOwnerReadRetainsAgentAttribution(t *testing.T) {
	e, call := mainOwnerFixture(t)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
	env := e.envelope("messaging.send", protocol.Target{Type: "message"}, nil, protocol.MessagingInput{Body: "Input to read"})
	sent, err := e.app.Execute(t.Context(), owner, &env, app.MessagingCommand{Action: "send"})
	if err != nil || sent.Error != nil {
		t.Fatal(sent, err)
	}
	var msg struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(sent.Result, &msg); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(protocol.MessagingReadPayload{ContentRevision: 1})
	response, err := ownerToolInvoke(t, e, call, "messaging.read", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "message", ID: msg.MessageID}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	reads, err := e.app.MessagingReceipts(t.Context(), msg.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reads: %+v", reads.Reads)
	for _, r := range reads.Reads {
		if r.Reader == "owner" {
			t.Error("Agent consumption recorded as owner read")
		}
	}
}
