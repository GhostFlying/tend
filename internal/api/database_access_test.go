package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestConcurrentOwnerReadPathsWithBoundedPool(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	id, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	paths := []string{"/api/v1/session", "/api/v1/issues?limit=20", "/api/v1/issues/" + issue.IssueID,
		"/api/v1/issues/" + issue.IssueID + "/activity", "/api/v1/conversation?after=0", "/api/v1/deployment"}
	handler := e.server.Handler()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 48 {
		wg.Go(func() {
			<-start
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, paths[i%len(paths)], nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Header().Get(deploymentHeader) != id || !json.Valid(w.Body.Bytes()) {
				t.Errorf("concurrent %s: status=%d identity=%q body=%s", paths[i%len(paths)], w.Code, w.Header().Get(deploymentHeader), w.Body.String())
			}
		})
	}
	// Readers keep independent snapshots while the sole writer commits actual
	// ledger rows. Waiting for a connection must not hold the writer hostage.
	wg.Go(func() {
		<-start
		for i := range 8 {
			if err := e.store.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO inbox(source,source_event_id,received_at) VALUES('read-concurrency',?,0)`, fmt.Sprint(i))
				return err
			}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	close(start)
	wg.Wait()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	stats := e.store.PoolStats()
	if stats.Reader.MaxOpenConnections == 0 || stats.Reader.OpenConnections > stats.Reader.MaxOpenConnections || stats.Writer.MaxOpenConnections != 1 {
		t.Fatalf("unbounded pool: %+v", stats)
	}
}
