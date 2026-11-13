package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHostedOrdinaryPublicationPurposeIsOptional(t *testing.T) {
	f := newHistoricalHostedDelivery(t)
	before := f.count(t, "artifacts")
	raw, _ := json.Marshal(hostedMutationArgs{EffectSeq: 1, TargetID: f.turn.RequestID, Payload: json.RawMessage(`{"body":"Ordinary message, no result registration"}`)})
	answer := f.e.svc.Handle(t.Context(), Call{Principal: f.principal, Native: "ordinary-publication", RequestID: f.ids.New(protocol.PrefixRequest), TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "conversation_notify", Arguments: raw})
	if !answer.Success {
		t.Fatal(answer)
	}
	if f.count(t, "artifacts") != before {
		t.Fatal("message changed formal result")
	}
	for _, spec := range NamedSpecs(agentconfig.IssueManager) {
		if spec.Name != "conversation_reply" && spec.Name != "conversation_notify" {
			continue
		}
		var schema map[string]any
		_ = json.Unmarshal(spec.InputSchema, &schema)
		payload := schema["properties"].(map[string]any)["payload"].(map[string]any)
		required, _ := payload["required"].([]any)
		if slices.Contains(required, any("purpose")) || slices.Contains(required, any("body")) {
			t.Fatal("native publication still requires choreography", payload)
		}
		if payload["allOf"] == nil {
			t.Fatal("optional marker contract missing", payload)
		}
	}
}
