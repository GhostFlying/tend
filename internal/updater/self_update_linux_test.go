//go:build linux

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSelfUpdateStaticAdmitsOnlyManagedRoles(t *testing.T) {
	for _, role := range []string{"server", "runner", "maintainer", "unknown"} {
		t.Run(role, func(t *testing.T) {
			cfg := Config{Role: role, StateDir: t.TempDir()}
			identity := Identity{PID: 10, StartTicks: 1, LaunchID: "original", SHA256: strings.Repeat("a", 64)}
			now := time.Unix(1000, 0)
			p := SelfUpdatePlan{ConfigFile: filepath.Join(cfg.StateDir, "absent-config.json"), UpdaterBinary: "/fixture/updater", Unit: "tend-fixture.service", Updater: identity, Child: identity, ConfigSHA256: strings.Repeat("b", 64), ManagerSHA256: strings.Repeat("c", 64), UnitSHA256: strings.Repeat("d", 64), LockDevice: 1, LockInode: 1}
			p.Request.Target.ExpiresAtMillis = now.Add(time.Minute).UnixMilli()
			err := selfPlanStatic(cfg, p, now, true)
			if role == "unknown" {
				if err == nil || errors.Is(err, os.ErrNotExist) {
					t.Fatal("unsupported role reached fixed configuration lookup", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal("managed role was rejected before fixed configuration lookup", err)
			}
		})
	}
}

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
