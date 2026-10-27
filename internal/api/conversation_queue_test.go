package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestQueueReadsAndWithdrawalAreOwnerOnly(t *testing.T) {
	e := newEnv(t)
	owner := e.login(protocol.ScopeOwner)
	agent := e.retiredAgentToken(clientID)
	e.server.cfg.HostedMainAgent = true
	env := e.envelope("conversation.submit_message", protocol.Target{Type: "conversation"}, nil, protocol.SubmitMessagePayload{Body: "queued"})
	var response protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", owner, env, &response); status != http.StatusOK || response.Operation.Status != protocol.OpAccepted {
		t.Fatalf("submit: %d %+v", status, response)
	}
	var queue protocol.ConversationQueuePage
	if status := e.do("GET", "/api/v1/conversation/queue", owner, nil, &queue); status != http.StatusOK || queue.Total != 1 || !queue.Items[0].CanWithdraw {
		t.Fatalf("queue: %d %+v", status, queue)
	}
	var transcript protocol.TranscriptPage
	if status := e.do("GET", "/api/v1/conversation/transcript", owner, nil, &transcript); status != 200 || len(transcript.Blocks) != 0 {
		t.Fatalf("transcript: %d %+v", status, transcript)
	}
	item := queue.Items[0]
	for _, token := range []string{"", agent} {
		if status := e.do("GET", "/api/v1/conversation/queue", token, nil, &queue); status != 401 && status != 403 {
			t.Fatalf("unauthorized queue: %d", status)
		}
	}
	withdraw := e.envelope("conversation.withdraw_message", protocol.Target{Type: "message", ID: item.Item.MessageID}, &item.Revision, protocol.WithdrawMessagePayload{})
	if status := e.do("POST", "/api/v1/ops", owner, withdraw, &response); status != 200 || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("withdraw: %d %+v", status, response)
	}
	if status := e.do("GET", "/api/v1/conversation/queue?category=withdrawn", owner, nil, &queue); status != 200 || queue.Total != 1 || queue.Items[0].Status != "withdrawn" {
		t.Fatalf("history: %d %+v", status, queue)
	}
}

func TestMissingTranscriptBlockIsNotFound(t *testing.T) {
	e := newEnv(t)
	owner := e.login(protocol.ScopeOwner)
	var response protocol.APIResponse
	if status := e.do("GET", "/api/v1/conversation/blocks/missing", owner, nil, &response); status != http.StatusNotFound {
		t.Fatalf("absent block: HTTP %d %+v", status, response)
	}
}
