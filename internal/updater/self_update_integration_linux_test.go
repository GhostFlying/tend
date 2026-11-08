//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// This opt-in fixture creates only its own units and children. It never targets
// an installed Tend service. Default unit tests do not access systemd.
func TestSelfUpdateSystemdRetainsServiceAndAdoptsSharedBinary(t *testing.T) {
	testSelfUpdateSystemd(t, false)
}

func TestSelfUpdateSystemdRejectsInterveningServiceOperation(t *testing.T) {
	testSelfUpdateSystemd(t, true)
}

func testSelfUpdateSystemd(t *testing.T, intervening bool) {
	if os.Getenv("TEND_TEST_SELF_SYSTEMD") != "1" {
		t.Skip("opt-in: isolated systemd user fixture and clean built release required")
	}
	bundle, err := release.Load(os.Getenv("TEND_TEST_SELF_BUNDLE"))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Set == nil || bundle.Set.Format != 2 {
		t.Fatal("supervisor-first bundle required")
	}
	old := os.Getenv("TEND_TEST_SELF_OLD")
	if !filepath.IsAbs(old) {
		t.Fatal("explicit private old fixture artifact required")
	}
	candidate, err := bundle.SetArtifact("updater", "linux", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	installer, err := release.RegularPath(bundle.Dir, candidate.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "tend-self-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	fixed := filepath.Join(root, "updater")
	if err = copyAtomic(old, fixed, 0o755); err != nil {
		t.Fatal(err)
	}
	oldSHA, err := hashFile(fixed)
	if err != nil {
		t.Fatal(err)
	}
	type instance struct {
		cfg          Config
		config, unit string
		child        Identity
		plan         SelfUpdatePlan
	}
	var instances []*instance
	// Cleanup is identity-bound and still runs on every failed assertion.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		for _, i := range instances {
			_, _ = (systemdUser{}).call(cleanup, "stop", i.unit)
			if i.child.PID > 1 {
				if err := (LinuxProcess{}).Stop(cleanup, i.child, 2*time.Second); err != nil {
					t.Error("fixture child cleanup", err)
				}
			}
			_, _ = (systemdUser{}).call(cleanup, "reset-failed", i.unit)
		}
	})
	wait := func(label string, check func() bool) {
		t.Helper()
		for ctx.Err() == nil {
			if check() {
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(30 * time.Millisecond):
			}
		}
		t.Fatal(label, ctx.Err())
	}
	for n, role := range []string{"server", "runner"} {
		dir := filepath.Join(root, role)
		if err = os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := Config{InstanceID: role + "-fixture", Role: role, StateDir: dir, InitialBinary: "/usr/bin/sleep", Args: []string{"300"}, LogPath: filepath.Join(root, role+".log"), Protocol: protocol.CurrentRunnerProtocol, StorageVersion: 1, StopTimeoutSeconds: 3, HealthTimeoutSeconds: 10}
		config := filepath.Join(root, role+".json")
		if err = writeJSON(config, cfg); err != nil {
			t.Fatal(err)
		}
		unit := "tend-self-fixture-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".service"
		i := &instance{cfg: cfg, config: config, unit: unit}
		instances = append(instances, i)
		command := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--user", "--unit="+unit, "--property=Type=simple", "--property=KillMode=process", "--property=Restart=on-failure", "--property=TimeoutStopSec=4s", "--", fixed, "serve", "--config", config)
		if raw, e := command.CombinedOutput(); e != nil {
			t.Fatalf("isolated fixture start: %v %s", e, raw)
		}
		wait("legacy fixture adopted service", func() bool {
			state, e := Call(ctx, cfg, "status", ControlRequest{})
			if e == nil {
				i.child = state.Process
			}
			return e == nil && i.child.PID > 1
		})
		evidence := filepath.Join(root, role+"-exit.json")
		if err = writeJSON(evidence, UpdaterExitEvidence{OperatorUID: os.Getuid(), ArtifactSHA256: oldSHA, Source: "testdata/self-supervisor", ReviewArtifact: "fixture source child-retention assertion", StateWritePolicy: "atomic-intent-before-effects", ExitPolicy: "retains-managed-child", ExpiresAtMillis: time.Now().Add(2 * time.Minute).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		requestID := "req_01M6F01JSS6Q9BFV6DJNSSBHEW"
		if n == 1 {
			requestID = "req_01M6F01JSS6Q9BFV6DJNSSBHEX"
		}
		i.plan, err = PrepareSelfUpdatePlan(ctx, cfg, SelfUpdatePlan{ConfigFile: config, Unit: unit, UpdaterBinary: fixed, ExitEvidence: evidence, Request: SelfUpdateRequest{Target: protocol.UpdaterSelfTarget{RequestID: requestID, ExpiresAtMillis: time.Now().Add(time.Minute).UnixMilli()}, UpgradeID: "fixture-upgrade", Release: bundle.Dir, ReleaseSetSHA256: bundle.SetSHA256}})
		if err != nil {
			t.Fatal("prepare legacy bootstrap", err)
		}
	}
	if intervening {
		i := instances[0]
		busy := filepath.Join(root, "busy-service")
		if err := os.Mkdir(busy, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, artifact := range bundle.Manifest.Artifacts {
			if err := copyAtomic(filepath.Join(bundle.Dir, artifact.Path), filepath.Join(busy, artifact.Path), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := copyAtomic(filepath.Join(bundle.Dir, "manifest.json"), filepath.Join(busy, "manifest.json"), 0o600); err != nil {
			t.Fatal(err)
		}
		staged, err := Call(ctx, i.cfg, "upgrade", ControlRequest{ID: "intervening-upgrade", Release: busy})
		if err != nil || staged.Operation == nil || staged.Operation.Phase != "staged" {
			t.Fatal("stage intervening service operation", staged, err)
		}
		if _, err := ApplySelfUpdate(ctx, i.cfg, i.plan); err == nil {
			t.Fatal("bootstrap accepted the intervening service operation")
		}
		manager, _, err := managerSnapshot(ctx, i.unit)
		if err != nil || manager["ActiveState"] != "active" {
			t.Fatal("rejection stopped the original supervisor", manager, err)
		}
		journal, err := SelfUpdateJournalStatus(i.cfg)
		if err != nil || journal.Phase != "rejected" {
			t.Fatal("pre-stop rejection was not durable", journal, err)
		}
		if err := selfChild(ctx, i.plan); err != nil {
			t.Fatal("rejection changed the service identity", err)
		}
		info, err := os.Stat(i.cfg.StateDir)
		if err != nil || info.Mode().Perm() != os.FileMode(i.plan.StateDirectoryMode).Perm() {
			t.Fatal("rejection did not restore original directory permissions", info, err)
		}
		t.Log("intervening operation rejected before stop; original supervisor and service retained, write fence restored")
		return
	}
	for _, i := range instances {
		planPath := filepath.Join(root, i.cfg.Role+"-plan.json")
		if err = writeJSON(planPath, i.plan); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, installer, "self-update", "apply", "--config", i.config, "--plan", planPath)
		if raw, e := command.CombinedOutput(); e != nil {
			t.Fatalf("legacy bootstrap %s: %v %s", i.cfg.Role, e, raw)
		}
		wait("new supervisor adoption receipt", func() bool {
			status, e := SelfUpdateCall(ctx, SocketPath(i.cfg), nil)
			return e == nil && status.Observation.Receipt != nil
		})
		status, e := SelfUpdateCall(ctx, SocketPath(i.cfg), nil)
		if e != nil {
			t.Fatal(e)
		}
		observed := status.Observation
		if observed.Process.SHA256 != candidate.SHA256 || observed.SupervisedProcess != i.child || observed.Receipt.RetainedProcess != i.child || observed.Receipt.Previous != i.plan.Updater || observed.Receipt.Installed != observed.Process {
			t.Fatal("shared path substituted per-instance process proof", observed)
		}
		if !selfOwnsFlock(ctx, observed.Process, i.plan.LockDevice, i.plan.LockInode) {
			t.Fatal("new updater did not own original lock", observed.Process)
		}
		if e = selfChild(ctx, i.plan); e != nil {
			t.Fatal("service identity changed during bootstrap", e)
		}
		t.Logf("%s child retained, original lock held by target supervisor; independent receipt verified", i.cfg.Role)
	}
	// The normal same-target path verifies the running process and produces a
	// new attempt's receipt without stopping its supervisor or service again.
	i := instances[0]
	before, e := SelfUpdateCall(ctx, SocketPath(i.cfg), nil)
	if e != nil {
		t.Fatal(e)
	}
	request := i.plan.Request
	request.Target.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHEY"
	observed, e := EnsureSelfUpdate(ctx, SocketPath(i.cfg), request)
	if e != nil {
		t.Fatal(e)
	}
	if observed.Process != before.Observation.Process || observed.SupervisedProcess != i.child || observed.Receipt.RequestID != request.Target.RequestID {
		t.Fatal("already-target replacement changed processes", observed)
	}
	history, err := os.ReadFile(filepath.Join(i.cfg.StateDir, "self-update-"+bytesDigest([]byte(i.plan.Request.Target.RequestID))+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var archived SelfUpdateJournal
	if err = json.Unmarshal(history, &archived); err != nil || archived.Phase != "verified" {
		t.Fatal("original attempt not retained", err)
	}
	if strings.Contains(string(history), "force") {
		t.Fatal("ordinary bootstrap borrowed force authority")
	}
	nextDir := os.Getenv("TEND_TEST_SELF_NEXT_BUNDLE")
	if nextDir == "" {
		return
	}
	nextBundle, e := release.Load(nextDir)
	if e != nil {
		t.Fatal(e)
	}
	nextArtifact, e := nextBundle.SetArtifact("updater", "linux", runtime.GOARCH)
	if e != nil || nextArtifact.SHA256 == candidate.SHA256 {
		t.Fatal("distinct automatic handoff candidate required", e)
	}
	for n, i := range instances {
		previous, e := SelfUpdateCall(ctx, SocketPath(i.cfg), nil)
		if e != nil {
			t.Fatal(e)
		}
		requestID := "req_01M6F01JSS6Q9BFV6DJNSSBHEZ"
		if n == 1 {
			requestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF0"
		}
		request := SelfUpdateRequest{Target: protocol.UpdaterSelfTarget{RequestID: requestID, ExpiresAtMillis: time.Now().Add(time.Minute).UnixMilli()}, UpgradeID: "fixture-automatic", Release: nextBundle.Dir, ReleaseSetSHA256: nextBundle.SetSHA256}
		adopted, e := EnsureSelfUpdate(ctx, SocketPath(i.cfg), request)
		if e != nil {
			t.Fatal("automatic independent installer", e)
		}
		if adopted.Process == previous.Observation.Process || adopted.Process.SHA256 != nextArtifact.SHA256 || adopted.SupervisedProcess != i.child || adopted.Receipt.Previous != previous.Observation.Process {
			t.Fatal("automatic handoff skipped actual process replacement", adopted)
		}
		if !selfOwnsFlock(ctx, adopted.Process, i.plan.LockDevice, i.plan.LockInode) {
			t.Fatal("automatic handoff lost original lock", adopted.Process)
		}
		t.Logf("%s automatic installer replaced its own process, retained child and original lock; shared-file proof is per instance", i.cfg.Role)
	}
}
