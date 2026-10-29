package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestMissingIssueContextReturnsNotFound(t *testing.T) {
	e := newEnv(t)
	var result protocol.APIResponse
	status := e.do("GET", "/api/v1/issues/missing/context", e.login(protocol.ScopeOwner), nil, &result)
	t.Logf("status=%d error=%+v", status, result.Error)
	if status != http.StatusNotFound {
		t.Error("missing context is reported as an internal server failure")
	}
}
