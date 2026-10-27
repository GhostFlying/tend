package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDeploymentIdentityRejectsMissingAndOldWritesBeforeAuthentication(t *testing.T) {
	e := newEnv(t)
	id, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.app.DeploymentID(t.Context())
	if err != nil || again != id {
		t.Fatal("identity changed", again, err)
	}
	for _, target := range []string{"", "old-deployment"} {
		r := httptest.NewRequest("POST", "/api/v1/ops", strings.NewReader(`{}`))
		r.Header.Set(deploymentHeader, target)
		w := httptest.NewRecorder()
		e.server.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusConflict || w.Header().Get(deploymentHeader) != id {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var session protocol.SessionView
	if status := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeOwner}, &session); status != 200 || session.DeploymentID != id {
		t.Fatal(status, session.DeploymentID)
	}
}
