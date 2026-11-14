package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
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
		_, err := tx.Exec(t.Context(), `ALTER TABLE deployment_identity RENAME TO identity_unavailable`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cold := New(h.store, h.ids, h.clock.Now)
	if id, err := cold.DeploymentID(t.Context()); err == nil || id != "" {
		t.Fatalf("new App database read failure: id=%q err=%v", id, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `ALTER TABLE identity_unavailable RENAME TO deployment_identity`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want, err := h.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cold.DeploymentID(t.Context()); err != nil || got != want {
		t.Fatalf("retry failed read: got=%q want=%q err=%v", got, want, err)
	}
}

func TestDeploymentIdentityCachedWithoutSQL(t *testing.T) {
	h := newHarness(t)
	id, err := h.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Make any identity SQL fail. The already loaded App needs no identity
	// query; a newly created App must still report the real database error.
	// This is fault injection, not a supported live ledger replacement.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TABLE deployment_identity`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got, err := h.app.DeploymentID(t.Context()); err != nil || got != id {
			t.Fatalf("warm identity used SQL: id=%q want=%q err=%v", got, id, err)
		}
	}
	if id, err := New(h.store, h.ids, h.clock.Now).DeploymentID(t.Context()); err == nil || id != "" {
		t.Fatalf("new App hid failed load: id=%q err=%v", id, err)
	}
}

func TestDeploymentIdentityWaiterCanCancelInitialization(t *testing.T) {
	h := newHarness(t)
	locked, release := make(chan struct{}), make(chan struct{})
	writer := make(chan error, 1)
	go func() {
		writer <- h.store.Write(t.Context(), func(*store.Tx) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	var finish sync.Once
	finishWriter := func() {
		finish.Do(func() {
			close(release)
			if err := <-writer; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(finishWriter)
	firstCtx, stopFirst := context.WithCancel(t.Context())
	defer stopFirst()
	first := make(chan error, 1)
	before := h.store.PoolStats().Writer.WaitCount
	go func() { _, err := h.app.DeploymentID(firstCtx); first <- err }()
	// The first load has observed absence and is now waiting on the writer.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for h.store.PoolStats().Writer.WaitCount == before {
		select {
		case err := <-first:
			t.Fatalf("load ended before writer wait: %v", err)
		case <-deadline.C:
			t.Fatal("load did not reach writer wait")
		case <-time.After(time.Millisecond):
		}
	}
	waitCtx, stopWait := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stopWait()
	if id, err := h.app.DeploymentID(waitCtx); !errors.Is(err, context.DeadlineExceeded) || id != "" {
		t.Fatalf("initialization waiter: id=%q err=%v", id, err)
	}
	stopFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A failed/cancelled initializer must not poison a later successful call.
	finishWriter()
	if id, err := h.app.DeploymentID(t.Context()); err != nil || id == "" {
		t.Fatalf("retry after cancellation: id=%q err=%v", id, err)
	}
}

func TestDeploymentIdentityStoreReopenAndSeparateLedgers(t *testing.T) {
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := openAppFixture(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := New(s, testkit.NewIDGen(clock, 1), clock.Now).DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if got, err := New(reopened, testkit.NewIDGen(clock, 2), clock.Now).DeploymentID(t.Context()); err != nil || got != id {
		t.Fatalf("reopened ledger: got=%q want=%q err=%v", got, id, err)
	}
	other, err := openAppFixture(t.Context(), filepath.Join(t.TempDir(), "other.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if got, err := New(other, testkit.NewIDGen(clock, 3), clock.Now).DeploymentID(t.Context()); err != nil || got == "" || got == id {
		t.Fatalf("different ledger shared cache: got=%q old=%q err=%v", got, id, err)
	}
}

func TestDeploymentIdentityCommitFailureCanRetry(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, sql := range []string{
			`CREATE TABLE identity_commit_probe (parent TEXT REFERENCES deployment_identity(deployment_id) DEFERRABLE INITIALLY DEFERRED)`,
			`CREATE TRIGGER reject_identity_commit AFTER INSERT ON deployment_identity BEGIN INSERT INTO identity_commit_probe VALUES ('missing-parent'); END`,
		} {
			if _, err := tx.Exec(t.Context(), sql); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := h.app.DeploymentID(t.Context()); err == nil || id != "" {
		t.Fatalf("failed commit published identity: id=%q err=%v", id, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM deployment_identity`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("failed commit left %d identities", count)
		}
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_identity_commit`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := h.app.DeploymentID(t.Context()); err != nil || !protocol.ValidID(protocol.Prefix("deployment"), id) {
		t.Fatalf("retry after commit failure: id=%q err=%v", id, err)
	}
}

func TestDeploymentIdentityConcurrentStoreInstances(t *testing.T) {
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "ledger.db")
	first, err := openAppFixture(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := store.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	apps := []*App{New(first, testkit.NewIDGen(clock, 1), clock.Now), New(second, testkit.NewIDGen(clock, 2), clock.Now)}
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 16)
	start := make(chan struct{})
	for i := range 16 {
		go func() { <-start; id, err := apps[i%2].DeploymentID(t.Context()); results <- result{id, err} }()
	}
	close(start)
	var want string
	for range 16 {
		r := <-results
		if r.err != nil || r.id == "" {
			t.Fatalf("initialization: %+v", r)
		}
		if want == "" {
			want = r.id
		}
		if r.id != want {
			t.Fatalf("separate Store identities differ: got=%q want=%q", r.id, want)
		}
	}
}
