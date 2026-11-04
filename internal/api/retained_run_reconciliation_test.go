package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedRunReconciliationReadRequiresOwner(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	path := "/api/v1/runs/missing/maintenance-reconciliation"
	if code := e.do(http.MethodGet, path, "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal("original Run evidence is not protected by owner authentication", code)
	}
	if code := e.do(http.MethodGet, path, token, nil, nil); code != http.StatusNotFound {
		t.Fatal("missing original Run should produce a typed missing result", code)
	}
}
