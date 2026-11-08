package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCheckerToolsRemovedAndUnregisteredBackgroundCallsRejected(t *testing.T) {
	e := newEnv(t)
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name == "background_submit" || spec.Name == "background_item_read" {
				t.Fatal("checker tool remains in current role", purpose, spec.Name)
			}
		}
	}
	for _, name := range []string{"background_submit", "background_item_read"} {
		answer := e.call(name, struct{}{}, "")
		if answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeInvalidRequest {
			t.Fatal("retired checker tool accepted", name, answer)
		}
	}
	result := e.svc.Handle(t.Context(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "retired-checker", Thread: "retired-thread", ConversationID: "work-item:retired"}, Tool: "conversation_context_index", Arguments: json.RawMessage(`{}`)})
	var answer answerView
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeForbidden {
		t.Fatal("unregistered checker bypassed named coordination", answer)
	}
}
