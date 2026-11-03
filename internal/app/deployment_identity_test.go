package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDeploymentIdentityConcurrentInitialization(t *testing.T) {
	h := newHarness(t)
	// Separate App instances must agree through the durable store, too.
	other := New(h.store, h.ids, h.clock.Now)
	const callers = 24
	type result struct {
		id  string
		err error
	}
	results := make(chan result, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		a := h.app
		if i%2 != 0 {
			a = other
		}
		wg.Go(func() {
			<-start
			id, err := a.DeploymentID(t.Context())
			results <- result{id, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var want string
	for r := range results {
		if r.err != nil || r.id == "" {
			t.Fatalf("initialization: id=%q err=%v", r.id, r.err)
		}
		if want == "" {
			want = r.id
		}
		if r.id != want {
			t.Fatalf("concurrent identities differ: %q != %q", r.id, want)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var id string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT deployment_id FROM deployment_identity WHERE singleton=1`).Scan(&id); err != nil {
			return err
		}
		if id != want {
			t.Errorf("durable identity = %q, want %q", id, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := New(h.store, h.ids, h.clock.Now).DeploymentID(t.Context()); err != nil || id != want {
		t.Fatalf("recreated App: id=%q want=%q err=%v", id, want, err)
	}
}

func TestDeploymentIdentityInitializationFailureCanRetry(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_identity BEFORE INSERT ON deployment_identity BEGIN SELECT RAISE(ABORT, 'injected initialization failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := h.app.DeploymentID(t.Context()); err == nil || id != "" {
		t.Fatalf("failed initialization published identity: id=%q err=%v", id, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_identity`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, err := h.app.DeploymentID(t.Context())
	if err != nil || !protocol.ValidID(protocol.Prefix("deployment"), id) {
		t.Fatalf("retry: id=%q err=%v", id, err)
	}
	if again, err := h.app.DeploymentID(t.Context()); err != nil || again != id {
		t.Fatalf("identity after retry: id=%q want=%q err=%v", again, id, err)
	}
}

func TestDeploymentIdentityReadFailureDoesNotPublishIdentity(t *testing.T) {
	h := newHarness(t)
	if _, err := h.app.DeploymentID(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if id, err := h.app.DeploymentID(ctx); !errors.Is(err, context.Canceled) || id != "" {
		t.Fatalf("canceled read: id=%q err=%v", id, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TABLE deployment_identity`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := h.app.DeploymentID(t.Context()); err == nil || id != "" {
		t.Fatalf("database read failure: id=%q err=%v", id, err)
	}
}
