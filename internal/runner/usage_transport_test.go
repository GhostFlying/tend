package runner

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
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
