package runner

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestPrivateUsageTransportKeepsHostAuthorityAndRejectsBusinessOperations(t *testing.T) {
	r := controlRunner(t)
	r.cfg.HostID, r.c.hostID, r.c.token = "host", "host", "runner-token"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Authorization") != "Bearer runner-token" || req.Header.Get(headerHost) != "host" {
			t.Error("proxy lost runner authority")
		}
		var env protocol.Envelope
		_ = json.NewDecoder(req.Body).Decode(&env)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`[]`)}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	call := func(operation, host string) int {
		raw, _ := json.Marshal(protocol.Envelope{RequestID: "original-request", Operation: operation, Target: protocol.Target{Type: "host", ID: host}, Payload: json.RawMessage(`{}`)})
		request := httptest.NewRequest("POST", "/runner/v1/rpc", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer untrusted-caller-value")
		response := httptest.NewRecorder()
		r.updaterHandler().ServeHTTP(response, request)
		return response.Code
	}
	if got := call(protocol.OpRunnerUsageSources, "host"); got != 503 {
		t.Fatal(got)
	}
	r.nativeUsageSupported = true
	if got := call(protocol.OpRunnerClaim, "host"); got != 403 {
		t.Fatal(got)
	}
	if got := call(protocol.OpRunnerUsageSources, "other"); got != 403 || calls != 0 {
		t.Fatal(got, calls)
	}
	if got := call(protocol.OpRunnerUsageSources, "host"); got != 200 || calls != 1 {
		t.Fatal(got, calls)
	}
}

func TestPrivateUsageJournalPreservesRealOwnershipAndReportsSafeContext(t *testing.T) {
	r := controlRunner(t)
	r.nativeUsageSupported = true
	original := journal.UsageSource{ID: "original-source", RunID: "original-run", ConversationID: "issue:original", Source: nativeusage.Source{Provider: "codex", ThreadID: "original-thread", SessionID: "original-session"}}
	if err := r.cfg.Journal.RegisterUsageSource(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"conversation", "run", "thread", "session"} {
		t.Run(field, func(t *testing.T) {
			candidate := original
			switch field {
			case "conversation":
				candidate.ConversationID = "issue:other"
			case "run":
				candidate.RunID = "other-run"
			case "thread":
				candidate.Source.ThreadID = "other-thread"
			case "session":
				candidate.Source.SessionID = "other-session"
			}
			raw, _ := json.Marshal(candidate)
			response := httptest.NewRecorder()
			r.updaterHandler().ServeHTTP(response, httptest.NewRequest("POST", "/runner/v1/usage/source", bytes.NewReader(raw)))
			var result protocol.APIResponse
			if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != protocol.CodeForbidden || result.Error.Message != "usage journal operation rejected" {
				t.Fatal(response.Code, response.Body.String())
			}
			var details map[string]string
			if json.Unmarshal(result.Error.Details, &details) != nil || details["stage"] != "register_source" || details["source_id"] != original.ID || details["run_id"] != candidate.RunID || details["thread_id"] != candidate.Source.ThreadID {
				t.Fatal("journal error lost actual attempted binding", details)
			}
		})
	}
	snapshot, err := r.cfg.Journal.UsageSourceSnapshot(t.Context(), original.RunID, "codex", original.Source.ThreadID)
	if err != nil || snapshot.Source.ConversationID != original.ConversationID || snapshot.Source.Source.SessionID != original.Source.SessionID {
		t.Fatal("rejected maintenance changed original ownership", snapshot, err)
	}
	input := journal.UsageQueueRequest{UsageSourceSnapshot: snapshot, Envelope: &protocol.Envelope{RequestID: "original-upload", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: r.cfg.HostID}, Payload: json.RawMessage(`{}`)}}
	input.ExpectedRevision++
	raw, _ := json.Marshal(input)
	response := httptest.NewRecorder()
	r.updaterHandler().ServeHTTP(response, httptest.NewRequest("POST", "/runner/v1/usage/queue", bytes.NewReader(raw)))
	var result protocol.APIResponse
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("real CAS was bypassed", response.Code, response.Body.String())
	}
	var details map[string]string
	if json.Unmarshal(result.Error.Details, &details) != nil || details["stage"] != "queue_usage" || details["request_id"] != "original-upload" {
		t.Fatal("original queue request lost", details)
	}
	if requests, err := r.cfg.Journal.Requests(t.Context()); err != nil || len(requests) != 0 {
		t.Fatal("rejected binding/CAS queued an upload", requests, err)
	}
}
