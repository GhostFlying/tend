package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDeploymentIdentityReadsWhileWriterBusy(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	id, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.store.Write(context.Background(), func(tx *store.Tx) error {
			close(locked) // BEGIN IMMEDIATE has acquired the writer connection.
			<-release
			return nil
		})
	}()
	t.Cleanup(func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-locked:
	case err := <-done:
		// Restore the value for cleanup.
		done <- err
		t.Fatalf("writer failed before acquiring transaction: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not acquire transaction")
	}
	for _, path := range []string{"/api/v1/session", "/api/v1/issues?limit=1", "/api/v1/deployment"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			e.server.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Header().Get(deploymentHeader) != id || !json.Valid(w.Body.Bytes()) || ctx.Err() != nil {
				t.Fatalf("GET with writer held: status=%d identity=%q body=%s context=%v", w.Code, w.Header().Get(deploymentHeader), w.Body.String(), ctx.Err())
			}
		})
	}
}

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
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ops", strings.NewReader(`{}`))
	r.Header.Set(deploymentHeader, id)
	w := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get(deploymentHeader) != id {
		t.Fatalf("matching identity must still authenticate: status=%d body=%s", w.Code, w.Body.String())
	}
	var session protocol.SessionView
	if status := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeOwner}, &session); status != 200 || session.DeploymentID != id {
		t.Fatal(status, session.DeploymentID)
	}
}
