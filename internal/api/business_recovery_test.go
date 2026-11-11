package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Reusing a trace never bypasses the actual object's control revision.
func assertOwnerStaleHTTP(t *testing.T, e *env, token string, envelope protocol.Envelope) protocol.APIResponse {
	t.Helper()
	status, response := e.op(token, envelope)
	if status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpFailed || response.Operation.Error == nil || response.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("old control input did not return stale: HTTP %d %+v", status, response)
	}
	return response
}
