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
	if err := ensureSelfJournalDirectory(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(filepath.Dir(selfPath(cfg)), "journal.lock")); err != nil {
		t.Fatal(err)
	}
	j, _ := selfEngineFixture()
	if err := writeSelf(cfg, j); err == nil {
		t.Fatal("symlink lock accepted")
	}
}

func TestSelfUpdateDiscoversInnermostSystemdService(t *testing.T) {
	for _, raw := range []string{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/tend-fixture.service\n", "3:cpu:/unrelated.service\n1:name=systemd:/user.slice/user-1000.slice/user@1000.service/app.slice/tend-fixture.service\n"} {
		unit, err := selfUnitFromCgroup(raw)
		if err != nil || unit != "tend-fixture.service" {
			t.Fatal("selected user manager instead of actual supervisor", unit, err)
		}
	}
	if _, err := selfUnitFromCgroup("0::/\n"); err == nil {
		t.Fatal("empty cgroup fabricated a service")
	}
}

func TestSelfUpdateAlreadyTargetReplacesOnlyArchivedVerifiedAttempt(t *testing.T) {
	cfg := Config{StateDir: t.TempDir()}
	j, _ := selfEngineFixture()
	j.Phase = "verified"
	j.Receipt = &protocol.UpdaterSelfReceipt{RequestID: j.Plan.Request.Target.RequestID}
	if err := writeSelf(cfg, j); err != nil {
		t.Fatal(err)
	}
	next := j
	next.Plan.Request.Target.RequestID = "next-original"
	next.Receipt = &protocol.UpdaterSelfReceipt{RequestID: next.Plan.Request.Target.RequestID}
	if err := writeSelf(cfg, next); err == nil {
		t.Fatal("prior receipt not archived")
	}
	if err := archiveSelf(cfg, j); err != nil {
		t.Fatal(err)
	}
	if err := writeSelf(cfg, next); err != nil {
		t.Fatal("verified same-target request rejected", err)
	}
	if err := archiveSelf(cfg, next); err != nil {
		t.Fatal(err)
	}
	if err := writeSelf(cfg, j); err == nil {
		t.Fatal("late historical receipt replaced newer attempt")
	}
}
func TestSelfUpdateHistoryDefersToNewIndependentMaintenance(t *testing.T) {
	cfg := Config{StateDir: t.TempDir()}
	j, _ := selfEngineFixture()
	j.Phase = "verified"
	j.Receipt = &protocol.UpdaterSelfReceipt{}
	if err := writeSelf(cfg, j); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedHeadPath(cfg), committedHandoffHead{MaintenanceID: "new-independent", PlanSHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	handled, err := AdoptSelfUpdate(t.Context(), &Supervisor{cfg: cfg})
	if handled || err != nil {
		t.Fatal("historical ordinary receipt intercepted new maintenance adopter", handled, err)
	}
}
