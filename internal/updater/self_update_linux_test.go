//go:build linux

package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSelfUpdateLateInstallerCannotOverwriteAdoption(t *testing.T) {
	cfg := Config{StateDir: t.TempDir()}
	j, _ := selfEngineFixture()
	j.Phase = "starting"
	if err := writeSelf(cfg, j); err != nil {
		t.Fatal(err)
	}
	adopted := j
	adopted.Phase = "verified"
	adopted.Receipt = &protocol.UpdaterSelfReceipt{RequestID: j.Plan.Request.Target.RequestID}
	if err := writeSelf(cfg, adopted); err != nil {
		t.Fatal(err)
	}
	j.LastError = "StartUnit response lost"
	if err := writeSelf(cfg, j); err != nil {
		t.Fatal(err)
	}
	actual, err := readSelf(cfg)
	if err != nil || actual.Phase != "verified" || actual.Receipt == nil || actual.LastError != "" {
		t.Fatal(actual, err)
	}
}
func TestSelfUpdateJournalSeparatesInstancesAndRequests(t *testing.T) {
	first := Config{StateDir: t.TempDir()}
	second := Config{StateDir: t.TempDir()}
	j, _ := selfEngineFixture()
	if err := writeSelf(first, j); err != nil {
		t.Fatal(err)
	}
	next := j
	next.Plan.Request.Target.RequestID = "other-request"
	if err := writeSelf(first, next); err == nil {
		t.Fatal("unfinished attempt overwritten")
	}
	if err := writeSelf(second, next); err != nil {
		t.Fatal("separate instance shared attempt", err)
	}
	wrong := j
	wrong.Plan.CandidateSHA256 = strings.Repeat("a", 64)
	if err := writeSelf(first, wrong); err == nil {
		t.Fatal("same request accepted new target")
	}
}
func TestSelfUpdateJournalRejectsUnsafeLock(t *testing.T) {
	cfg := Config{StateDir: t.TempDir()}
	target := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cfg.StateDir, "self-journal.lock")); err != nil {
		t.Fatal(err)
	}
	j, _ := selfEngineFixture()
	if err := writeSelf(cfg, j); err == nil {
		t.Fatal("symlink lock accepted")
	}
}
