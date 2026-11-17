//go:build linux

package updater

import (
	"os"
	"testing"
)

func TestBinaryOnlyUpdatePreservesDataWithoutSnapshot(t *testing.T) {
	h := newHarness(t)
	// The candidate uses journal 9. Both processes already use that format.
	h.s.state.Current.StorageVersion = 9
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	h.upgrade(t)
	h.until(t, "checking")
	if h.s.Status().Operation.BackupDir != "" || len(h.s.Status().Operation.Backups) != 0 {
		t.Fatal("binary-only replacement copied service data")
	}
	// A failed candidate can restore the old executable without overwriting
	// same-format data, which has no migration snapshot to restore.
	h.a.unhealthy, h.a.rollback = true, true
	h.now = h.now.Add(11e9)
	h.until(t, "rolled_back")
	body, err := os.ReadFile(h.data)
	if err != nil || string(body) != "old ledger bytes" || h.p.starts != 2 {
		t.Fatal("binary rollback changed data or failed to restore service", string(body), err)
	}
}
