//go:build linux

package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func configurationHarness(t *testing.T) (*harness, protocol.RunnerConfiguration, string) {
	t.Helper()
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "runner.json")
	candidate := filepath.Join(t.TempDir(), "candidate.json")
	for p, b := range map[string]string{path: `{"providers":[]}`, candidate: `{"providers":["github"]}`} {
		if err := os.WriteFile(p, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h.opts.ValidateConfiguration = func(context.Context, string, string, string) error { return nil }
	h.cfg.ServiceConfig = path
	h.cfg.Args = []string{"-config", path}
	h.reopen(t)
	h.bundle = makeBundle(t, []byte("old executable"))
	// Configuration maintenance retains the installed protocol and storage.
	b, err := release.Load(h.bundle)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := hashFile(path)
	after, _ := hashFile(candidate)
	target := protocol.RunnerConfiguration{PreviousSHA256: before, SHA256: after, Providers: []string{"github"}}
	if err := h.s.StageConfiguration(t.Context(), "configuration-1", b.ManifestSHA256, candidate, &target); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UpgradeConfigured(t.Context(), "configuration-1", h.bundle, &target); err != nil {
		t.Fatal(err)
	}
	return h, target, candidate
}

func TestConfigurationMaintenanceSameBinaryAndCrashRecovery(t *testing.T) {
	h, target, _ := configurationHarness(t)
	h.until(t, "config_applying")
	if h.p.stops != 1 || h.p.starts != 0 {
		t.Fatal("configuration did not wait for stop")
	}
	// Simulate a crash between the atomic file replacement and the phase write.
	if err := h.s.applyConfiguration(h.s.Status().Operation.Configuration); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	h.until(t, "committed")
	if sum, _ := hashFile(h.cfg.ServiceConfig); sum != target.SHA256 {
		t.Fatal("candidate configuration not installed")
	}
	if h.p.starts != 1 || h.p.stops != 1 {
		t.Fatal("same binary was skipped or replayed", h.p.starts, h.p.stops)
	}
	old := h.s.Status().Process
	h.reopen(t)
	if err := h.s.UpgradeConfigured(t.Context(), "configuration-1", h.bundle, &target); err != nil {
		t.Fatal(err)
	}
	h.step(t)
	if h.s.Status().Process != old || h.p.starts != 1 {
		t.Fatal("committed configuration was replayed")
	}
}

func TestConfigurationMaintenanceRollbackRestoresOriginal(t *testing.T) {
	h, target, _ := configurationHarness(t)
	h.until(t, "checking")
	h.a.unhealthy = true
	h.a.rollback = true
	h.now = h.now.Add(11 * time.Second)
	h.until(t, "rolled_back")
	if sum, _ := hashFile(h.cfg.ServiceConfig); sum != target.PreviousSHA256 {
		t.Fatal("configuration rollback did not restore original")
	}
	if h.p.starts != 2 {
		t.Fatal("old binary not restarted", h.p.starts)
	}
}

func TestConfigurationMaintenanceExpiredGrantRestoresAppliedFile(t *testing.T) {
	h, target, _ := configurationHarness(t)
	h.until(t, "config_applying")
	if err := h.s.applyConfiguration(h.s.Status().Operation.Configuration); err != nil {
		t.Fatal(err)
	}
	h.boot += 20000
	h.reopen(t)
	h.until(t, "rolled_back")
	if sum, _ := hashFile(h.cfg.ServiceConfig); sum != target.PreviousSHA256 {
		t.Fatal("expired prelaunch operation left candidate configuration")
	}
}

func TestConfigurationMaintenanceConflictsAndUnknownStart(t *testing.T) {
	t.Run("stage conflict", func(t *testing.T) {
		h, target, candidate := configurationHarness(t)
		b, _ := release.Load(h.bundle)
		changed := target
		changed.Providers = []string{"codebase"}
		if err := h.s.StageConfiguration(t.Context(), "configuration-1", b.ManifestSHA256, candidate, &changed); err == nil {
			t.Fatal("stage payload changed")
		}
		if err := h.s.Upgrade(t.Context(), "configuration-1", h.bundle); err == nil {
			t.Fatal("configuration silently omitted")
		}
		if err := os.WriteFile(h.cfg.ServiceConfig, []byte("concurrent edit"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Step(t.Context()); err == nil || h.p.stops != 0 {
			t.Fatal("configuration CAS did not guard stop")
		}
	})
	t.Run("unknown start", func(t *testing.T) {
		h, _, _ := configurationHarness(t)
		h.until(t, "config_applying")
		h.p.ambiguousStart = true
		if err := h.s.Step(t.Context()); err != ErrUnknown {
			t.Fatal(err)
		}
		h.reopen(t)
		h.until(t, "committed")
		if h.p.starts != 1 {
			t.Fatal("uncertain launch repeated")
		}
	})
	t.Run("definite launch rejection", func(t *testing.T) {
		h, target, _ := configurationHarness(t)
		h.until(t, "config_applying")
		h.p.rejectStart = true
		if err := h.s.Step(context.Background()); err != ErrNotStarted {
			t.Fatal(err)
		}
		h.reopen(t)
		h.until(t, "rolled_back")
		if sum, _ := hashFile(h.cfg.ServiceConfig); sum != target.PreviousSHA256 {
			t.Fatal("rejected launch retained new configuration")
		}
	})
}

func TestConfigurationStatusIsDetachedFromUncommittedState(t *testing.T) {
	h, _, _ := configurationHarness(t)
	snapshot := h.s.Status()
	staged := snapshot.Configurations["configuration-1"]
	staged.Target.Providers[0] = "corrupt"
	snapshot.Configurations["unpersisted"] = staged
	snapshot.Operation.Configuration.Target.Providers[0] = "corrupt-operation"
	if got := h.s.Status(); got.Configurations["configuration-1"].Target.Providers[0] != "github" || got.Operation.Configuration.Target.Providers[0] != "github" {
		t.Fatal("status aliases internal durable configuration")
	}
	h.until(t, "committed")
	// A failed state write cannot appear through the durable status response.
	h.s.writeState = func(string, State) error { return os.ErrPermission }
	// Directly exercise the same staged map mutation/save boundary with no filesystem side effects.
	h.s.state.Configurations["unpersisted"] = staged
	if err := h.s.save(); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, ok := h.s.Status().Configurations["unpersisted"]; ok {
		t.Fatal("failed staging published a durable fact")
	}
}
