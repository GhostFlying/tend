package serverkit

import (
	"context"
	"github.com/GhostFlying/tend/internal/store"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/testkit/storefixture"
	"github.com/GhostFlying/tend/internal/testkit/testphase"
)

var assemblyFixture storefixture.Template

// Functional assembly tests reopen independent, current-schema ledgers. Real
// cold startup, migration failures and retained reopen are covered separately.
func prepareAssemblyData(t *testing.T, now func() time.Time) string {
	t.Helper()
	ctx, finish := testphase.Context(t, t.Context(), "assembly fixture", 10*time.Minute)
	defer finish()
	data := t.TempDir()
	db, err := assemblyFixture.Open(ctx, filepath.Join(data, "tend.db"), now)
	if err != nil {
		t.Fatal("prepare assembly fixture:", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal("close assembly fixture:", err)
	}
	return data
}

// Functional data can live on a spacious build/test volume. IPC must have its
// own actual short pathname, including the default archive-control filename.
func assemblySocket(t *testing.T) string {
	t.Helper()
	return testkit.SocketPath(t, "upgrades.sock")
}

func openAssemblyLedger(t *testing.T, parent context.Context, path string, now func() time.Time) (*store.Store, error) {
	t.Helper()
	ctx, finish := testphase.Context(t, parent, "assembly ledger", 10*time.Minute)
	defer finish()
	return assemblyFixture.Open(ctx, path, now)
}
