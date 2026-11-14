package serverkit

import (
	"path/filepath"
	"testing"
	"time"

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
