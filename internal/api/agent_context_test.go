package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"testing"
)

func TestMissingWorkItemContextReturnsNotFound(t *testing.T) {
	e := newEnv(t)
	var result protocol.APIResponse
	status := e.do("GET", "/api/v1/work-items/missing/context", e.login(protocol.ScopeOwner), nil, &result)
	t.Logf("status=%d error=%+v", status, result.Error)
	if status != http.StatusNotFound {
		t.Error("missing context is reported as an internal server failure")
	}
}
