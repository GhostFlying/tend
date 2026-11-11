package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManualSourceReadDistinguishesInjectedSourceFromOrdinaryMessage(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	token := e.login(protocol.ScopeOwner)
	status, response := e.op(token, e.envelope("conversation.submit_message", protocol.Target{Type: "message"}, nil,
		protocol.SubmitMessagePayload{Body: "ordinary owner discussion"}))
	if status != http.StatusOK || response.Operation == nil || response.Operation.Error != nil {
		t.Fatalf("submit ordinary message: %d %+v", status, response)
	}
	var ordinary struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(response.Operation.Result, &ordinary); err != nil || ordinary.MessageID == "" {
		t.Fatal("ordinary message was not persisted", err)
	}
	injection := protocol.InjectSourcePayload{Text: "retained external original", Metadata: protocol.ManualSourceMetadata{URL: "https://example.com/original"}}
	status, response = e.op(token, e.envelope("conversation.inject_source", protocol.Target{Type: "conversation", ID: "main"}, nil, injection))
	if status != http.StatusOK || response.Operation == nil || response.Operation.Error != nil {
		t.Fatalf("inject manual source: %d %+v", status, response)
	}
	var injected protocol.ManualSource
	if err := json.Unmarshal(response.Operation.Result, &injected); err != nil || injected.MessageID == "" {
		t.Fatal("injected source was not persisted", err)
	}
	beforeEvents := e.count(`SELECT COUNT(*) FROM events`)
	beforeOutbox := e.count(`SELECT COUNT(*) FROM outbox`)
	for _, messageID := range []string{ordinary.MessageID, e.ids.New(protocol.PrefixMessage)} {
		var missing protocol.APIResponse
		if status := e.do(http.MethodGet, "/api/v1/sources/"+messageID, token, nil, &missing); status != http.StatusNotFound || missing.Error == nil || missing.Error.Code != protocol.CodeNotFound {
			t.Errorf("source %s: status=%d response=%+v, want 404 not_found", messageID, status, missing)
		}
	}
	var read protocol.ManualSource
	if status := e.do(http.MethodGet, "/api/v1/sources/"+injected.MessageID, token, nil, &read); status != http.StatusOK || read.MessageID != injected.MessageID || read.Text != injection.Text || read.Metadata.URL != injection.Metadata.URL || read.SuppliedBy.Subject != ownerLogin {
		t.Fatalf("original manual source changed: status=%d source=%+v", status, read)
	}
	var session protocol.SessionView
	if status := e.do(http.MethodGet, "/api/v1/session", token, nil, &session); status != http.StatusOK || session.Login != ownerLogin {
		t.Fatalf("missing source invalidated the owner session: status=%d session=%+v", status, session)
	}
	if e.count(`SELECT COUNT(*) FROM events`) != beforeEvents || e.count(`SELECT COUNT(*) FROM outbox`) != beforeOutbox || e.count(`SELECT COUNT(*) FROM manual_message_sources`) != 1 {
		t.Fatal("source reads changed requests, effects or manual-source membership")
	}
}
