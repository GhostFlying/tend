package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHostedAgentConversationListReportsBackgroundCapability(t *testing.T) {
	e := newEnv(t)
	answer := e.call("agent_conversations_list", agentListArgs{}, "")
	if !answer.OK {
		t.Fatalf("list conversations: %+v", answer.Error)
	}
	var page protocol.AgentConversationPage
	if err := json.Unmarshal(answer.Result, &page); err != nil {
		t.Fatal(err)
	}
	if !page.RuntimeAvailable || len(page.Items) != 0 {
		t.Fatalf("hosted empty list must retain background capability: %+v", page)
	}
}

func TestAgentConversationListRetainsPrincipalBoundary(t *testing.T) {
	e := newEnv(t)
	for name, principal := range map[string]protocol.Principal{
		"retired external": {Kind: protocol.PrincipalMainAgent, Subject: "external-client", Thread: "external-thread", Agent: protocol.AgentCodex},
		"background":       {Kind: protocol.PrincipalBackgroundAgent, Subject: "harness", Thread: "background-thread", ConversationID: "work-item:fixture"},
	} {
		t.Run(name, func(t *testing.T) {
			result := e.svc.Handle(context.Background(), Call{Principal: principal, Tool: "agent_conversations_list", Arguments: json.RawMessage(`{}`)})
			var answer answerView
			if err := json.Unmarshal([]byte(result.Text), &answer); err != nil {
				t.Fatal(err)
			}
			if result.Success || answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeForbidden {
				t.Fatalf("unexpected access: %s", result.Text)
			}
		})
	}
}
