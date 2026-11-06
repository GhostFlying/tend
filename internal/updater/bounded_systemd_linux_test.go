//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func TestMain(m *testing.M) {
	if os.Getenv("TEND_BOUNDED_UPDATER_HELPER") == "1" && len(os.Args) == 4 && os.Args[1] == "serve" && os.Args[2] == "--config" && filepath.IsAbs(os.Args[3]) {
		if err := boundedMutexUpdaterHelper(os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func boundedMutexUpdaterHelper(configFile string) error {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.InstanceID != "bounded-fixture" || filepath.Dir(cfg.StateDir) != filepath.Dir(configFile) || !strings.HasPrefix(filepath.Base(filepath.Dir(cfg.StateDir)), "tend-bounded-fixture-") {
		return errors.New("updater helper accepts only its isolated fixture configuration")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	authority, err := NewHTTPAuthority(cfg)
	if err != nil {
		return err
	}
	s, err := Open(cfg, Options{Authority: authority})
	if err != nil {
		return err
	}
	defer s.Close()
	monitorDone := make(chan struct{})
	defer func() { cancel(); <-monitorDone }()
	go func() {
		defer close(monitorDone)
		for ctx.Err() == nil {
			if _, err := os.Stat(filepath.Join(cfg.StateDir, "fixture-hang-mutex")); err == nil {
				// This fault exists only in this test executable. A manual unit
				// stop cannot release this mutex or obtain graceful cooperation.
				signal.Ignore(syscall.SIGTERM)
				s.mu.Lock()
				_ = os.WriteFile(filepath.Join(cfg.StateDir, "fixture-mutex-held"), []byte("held\n"), 0o600)
				<-ctx.Done()
				s.mu.Unlock()
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	return s.Serve(ctx)
}

// TestBoundedSystemdProcessHelper is an isolated child of this test's updater.
// It has no service endpoints, credentials, application database or journal.
func TestBoundedSystemdProcessHelper(t *testing.T) {
	if os.Getenv("TEND_BOUNDED_PROCESS_HELPER") != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	defer signal.Stop(ch)
	<-ch
	os.Exit(0)
}

// This opt-in fixture deliberately initializes its own synthetic maintenance
// history. The identities, systemd policy, lock and process effects are real;
// it does not claim an application deployment or a production committed proof.
func TestBoundedSystemdExternalTakeoverRetainsChild(t *testing.T) {
	runBoundedSystemdTakeoverFixture(t, false, "server")
}

func TestBoundedSystemdMutexHungUpdaterTakeover(t *testing.T) {
	runBoundedSystemdTakeoverFixture(t, true, "server")
}

func TestBoundedSystemdRunnerMutexHungUpdaterTakeover(t *testing.T) {
	runBoundedSystemdTakeoverFixture(t, true, "runner")
}

func runBoundedSystemdTakeoverFixture(t *testing.T, mutexHung bool, role string) {
	if os.Getenv("TEND_TEST_SYSTEMD_USER") != "1" {
		t.Skip("opt-in: isolated user-systemd unit and exact updater artifact required")
	}
	updaterBinary := os.Getenv("TEND_TEST_NEW_UPDATER")
	if !filepath.IsAbs(updaterBinary) {
		t.Fatal("TEND_TEST_NEW_UPDATER must name the exact absolute fixture artifact")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "tend-bounded-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixed := filepath.Join(root, "fixed-updater")
	sourceUpdater := updaterBinary
	if mutexHung {
		sourceUpdater = self
	}
	if err := copyAtomic(sourceUpdater, fixed, 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, "helper.env")
	if err := os.WriteFile(envFile, []byte("TEND_BOUNDED_PROCESS_HELPER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(root, "empty-fixture.db")
	if err := os.WriteFile(dataFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{InstanceID: "bounded-fixture", Role: role, StateDir: stateDir, InitialBinary: self, Args: []string{"-test.run=^TestBoundedSystemdProcessHelper$"}, EnvFiles: []string{envFile}, DataFiles: []string{dataFile}, LogPath: filepath.Join(root, "child.log"), AuthoritySocket: filepath.Join(root, "absent-authority.sock"), CoordinatorSocket: filepath.Join(root, "absent-authority.sock"), AuthorizeURL: "http://localhost/updater/authorize", HealthURL: "http://localhost/updater/health", Protocol: protocol.CurrentRunnerProtocol, StorageVersion: 1, StopTimeoutSeconds: 2, HealthTimeoutSeconds: 3}
	normalizeConfig(&cfg)
	configFile := filepath.Join(root, "updater.json")
	if err := writeJSON(configFile, cfg); err != nil {
		t.Fatal(err)
	}
	serverSHA, err := hashFile(self)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	version := Version{Protocol: cfg.Protocol, StorageVersion: 1, ReleaseID: "bounded-fixture-source", ManifestSHA256: strings.Repeat("a", 64), Binary: self, Artifact: release.Artifact{Role: role, OS: runtime.GOOS, Arch: runtime.GOARCH, Path: role, SHA256: serverSHA, Size: info.Size()}}
	statePath := filepath.Join(stateDir, "state.json")
	if err := writeJSON(statePath, State{Format: 1, InstanceID: cfg.InstanceID, Current: version}); err != nil {
		t.Fatal(err)
	}
	unit := "tend-bounded-fixture-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".service"
	var child, sentinel Identity
	launch := "bounded-fixture-successor-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, e := (systemdUser{}).call(cleanup, "stop", unit); e != nil {
			t.Errorf("stop exact fixture unit: %v", e)
		}
		ids := []Identity{child, sentinel}
		if found, e := (LinuxProcess{}).Find(cleanup, launch, serverSHA); e == nil {
			ids = append(ids, found...)
		} else {
			t.Errorf("reconcile exact fixture successor during cleanup: %v", e)
		}
		for _, id := range ids {
			if id.PID > 1 {
				if e := (LinuxProcess{}).Stop(cleanup, id, time.Second); e != nil {
					t.Errorf("stop exact fixture child: %v", e)
				}
				if actual, e := (LinuxProcess{}).Inspect(cleanup, id.PID); e == nil && actual == id {
					t.Error("fixture child remained alive after cleanup")
				}
			}
		}
		_, _ = (systemdUser{}).call(cleanup, "reset-failed", unit)
	})
	unitArgs := []string{"--user", "--unit=" + unit, "--property=Type=simple", "--property=KillMode=process", "--property=Restart=on-failure", "--property=TimeoutStopSec=4s"}
	if mutexHung {
		unitArgs = append(unitArgs, "--setenv=TEND_BOUNDED_UPDATER_HELPER=1")
	}
	unitArgs = append(unitArgs, "--", fixed, "serve", "--config", configFile)
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", unitArgs...)
	if raw, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("start exact isolated fixture unit: %v %s", e, raw)
	}
	var st State
	boundedSystemdWait(t, ctx, "actual updater child startup", func() bool {
		var e error
		st, e = Call(ctx, cfg, "status", ControlRequest{})
		if e == nil && st.Process.PID > 1 && st.Operation != nil && st.Operation.Phase == "committed" {
			child = st.Process
			return true
		}
		return false
	})
	if _, err := (systemdUser{}).call(ctx, "stop", unit); err != nil {
		t.Fatal(err)
	}
	if actual, e := (LinuxProcess{}).Inspect(ctx, child.PID); e != nil || actual != child {
		t.Fatal("fixture initialization lost its real retained child", e)
	}
	updaterSHA, err := hashFile(updaterBinary)
	if err != nil {
		t.Fatal(err)
	}
	manager, policy, err := managerSnapshot(ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	unitSHA, err := unitDigest(ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, err := lockIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(10 * time.Minute).UnixMilli()
	previous := Binding{UpgradeID: "fixture-previous", InstanceID: cfg.InstanceID, ProcessInstanceID: child.LaunchID, ReleaseID: version.ReleaseID, ManifestSHA256: version.ManifestSHA256, ArtifactSHA256: serverSHA}
	maintenance := protocol.CommittedMaintenancePlan{MaintenanceID: "bounded-systemd-fixture", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEV", LinkRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEW", Previous: previous, PreviousProcess: child, ConfigSHA256: digest(cfg), Successor: Binding{UpgradeID: "bounded-systemd-fixture", InstanceID: cfg.InstanceID, ProcessInstanceID: launch, ReleaseID: "fixture-target", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: serverSHA}, OperatorUID: os.Getuid(), WindowClaim: "isolated-bounded-fixture", ExpiresAtMillis: expires}
	configRaw, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	plan := CommittedHandoffPlan{Maintenance: maintenance, Unit: unit, UnitSHA256: unitSHA, ManagerSHA256: policy, ManagerControlGroup: manager["ControlGroup"], UpdaterBinary: fixed, ConfigFile: configFile, ConfigFileSHA256: bytesDigest(configRaw), Candidate: updaterBinary, CandidateSHA256: updaterSHA, LockDevice: dev, LockInode: ino}
	st.Format, st.Generation, st.UpdaterEpoch = 1, 10, "synthetic-fixture-epoch"
	st.Operation = nil
	st.CommittedProof = &CommittedProof{Binding: previous, Version: version, Process: child}
	if role == "server" {
		st.Format = 3
		st.Maintenance = &CommittedMaintenance{Plan: maintenance, Phase: "updater_adopted", Target: version, OriginalProof: *st.CommittedProof}
	}
	if err := writeJSON(statePath, st); err != nil {
		t.Fatal(err)
	}
	if role == "server" {
		if err := os.Mkdir(filepath.Join(stateDir, "committed-handoffs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(committedJournalPath(cfg, maintenance.MaintenanceID), CommittedHandoffJournal{Plan: plan, Phase: "updater_adopted", Generation: 1}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(committedHeadPath(cfg), committedHandoffHead{MaintenanceID: maintenance.MaintenanceID, PlanSHA256: digest(plan)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (systemdUser{}).call(ctx, "start", unit); err != nil {
		t.Fatal(err)
	}
	boundedSystemdWait(t, ctx, "synthetic maintenance with real original process", func() bool {
		var e error
		st, e = Call(ctx, cfg, "status", ControlRequest{})
		return e == nil && (role == "runner" && st.Process == child || st.Maintenance != nil && st.Maintenance.Phase == "updater_adopted")
	})
	manager, _, err = managerSnapshot(ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(manager["MainPID"])
	if err != nil {
		t.Fatal(err)
	}
	oldUpdater, err := (LinuxProcess{}).Inspect(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	plan.Updater = oldUpdater
	sentinel = boundedRealHelper(t, ctx, root, "sentinel")
	phase := "idle"
	if st.Maintenance != nil {
		phase = st.Maintenance.Phase
	}
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: st.Generation, ExpectedPhase: phase, PlanSHA256: digest(maintenance), Role: role, Source: child, Target: maintenance.Successor, Reason: "isolated external takeover of an unresponsive updater", ExpiresAtMillis: expires, Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(), UpdaterSource: &oldUpdater, UpdaterLock: &protocol.BoundedDataIdentity{Device: dev, Inode: ino}}
	if role == "server" {
		auth.DataIdentity, err = boundedServerDataIdentity(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	planFile, authFile := filepath.Join(root, "plan.json"), filepath.Join(root, "authorization.json")
	var inputPlan any = plan
	if role == "runner" {
		grant := boundedSystemdRunnerGrant(t, cfg, plan, child, auth)
		auth = grant.Plan.Authorization
		inputPlan = grant
	}
	if err := writeJSON(planFile, inputPlan); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(authFile, auth); err != nil {
		t.Fatal(err)
	}
	if mutexHung {
		if err := os.WriteFile(filepath.Join(stateDir, "fixture-hang-mutex"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		boundedSystemdWait(t, ctx, "test updater actually holds its business mutex", func() bool {
			_, err := os.Stat(filepath.Join(stateDir, "fixture-mutex-held"))
			return err == nil
		})
	} else {
		boundedFixtureFreeze(t, ctx, oldUpdater)
	}
	rpcCtx, rpcDone := context.WithTimeout(ctx, 100*time.Millisecond)
	_, rpcErr := Call(rpcCtx, cfg, "status", ControlRequest{})
	rpcDone()
	if rpcErr == nil {
		t.Fatal("fixture updater control unexpectedly remained responsive")
	}
	// Observe the real boundary independently of installer return timing. The
	// new supervisor may subsequently stop the service under the force grant.
	preserved := make(chan error, 1)
	observerDone := make(chan struct{})
	observerCtx, observerCancel := context.WithCancel(ctx)
	defer func() { observerCancel(); <-observerDone }()
	go func() {
		defer close(observerDone)
		for observerCtx.Err() == nil {
			_, e := (LinuxProcess{}).Inspect(observerCtx, oldUpdater.PID)
			if errors.Is(e, ErrNotRunning) {
				actual, childErr := (LinuxProcess{}).Inspect(observerCtx, child.PID)
				if childErr != nil || actual != child {
					preserved <- fmt.Errorf("old updater exited without exact child retained: %v", childErr)
				} else {
					preserved <- nil
				}
				return
			}
			select {
			case <-observerCtx.Done():
			case <-time.After(5 * time.Millisecond):
			}
		}
		preserved <- observerCtx.Err()
	}()
	result, err := BoundedCommand(ctx, cfg, "takeover", planFile, authFile, auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase)
	if err != nil {
		// Preserve the failure before fixture cleanup removes its private root.
		manager, _, managerErr := managerSnapshot(ctx, unit)
		owner, ownerKnown := flockOwner(dev, ino)
		stateRaw, stateErr := os.ReadFile(statePath)
		journalRaw, journalErr := os.ReadFile(boundedInstallerPath(cfg, auth.RequestID))
		var ownerIdentity Identity
		if owner > 1 {
			ownerIdentity, _ = (LinuxProcess{}).Inspect(ctx, owner)
		}
		boundedFixtureEvidence(t, role+"-takeover-failure", map[string]any{"unit": unit, "error": err.Error(), "original_updater": oldUpdater, "original_child": child, "lock_device": dev, "lock_inode": ino, "reported_lock_owner": owner, "lock_owner_known": ownerKnown, "lock_owner_identity": ownerIdentity, "manager": manager, "manager_error": fmt.Sprint(managerErr), "state_json": string(stateRaw), "state_read_error": fmt.Sprint(stateErr), "installer_journal_json": string(journalRaw), "installer_journal_read_error": fmt.Sprint(journalErr), "installer_result": result})
		t.Fatalf("external takeover failed: %v; original state: %+v", err, result)
	}
	if err := <-preserved; err != nil {
		t.Fatal(err)
	}
	var newUpdater Identity
	boundedSystemdWait(t, ctx, "new exact manager owns original lock", func() bool {
		m, _, e := managerSnapshot(ctx, unit)
		if e != nil {
			return false
		}
		pid, e := strconv.Atoi(m["MainPID"])
		if e != nil || pid < 2 || pid == oldUpdater.PID {
			return false
		}
		newUpdater, e = (LinuxProcess{}).Inspect(ctx, pid)
		if e != nil {
			return false
		}
		owns, e := processOwnsFlock(ctx, "/proc", pid, dev, ino)
		return e == nil && owns
	})
	if newDev, newIno, e := lockIdentity(stateDir); e != nil || newDev != dev || newIno != ino {
		t.Fatal("external takeover replaced its original lock inode", e)
	}
	if competing, e := Open(cfg, Options{}); e == nil {
		_ = competing.Close()
		t.Fatal("a second writer acquired the live supervisor lock")
	}
	latest, err := readState(statePath)
	if err != nil || latest.Bounded == nil || digest(latest.Bounded.Authorizations[auth.RequestID]) != digest(auth) {
		t.Fatal("external takeover changed the original authority or CAS", err)
	}
	if actual, e := (LinuxProcess{}).Inspect(ctx, sentinel.PID); e != nil || actual != sentinel {
		t.Fatal("external takeover signaled an unrelated process", e)
	}
	if _, e := BoundedCommand(ctx, cfg, "takeover", planFile, authFile, auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase); e == nil {
		t.Fatal("replayed stale takeover was executed again")
	}
	if actual, e := (LinuxProcess{}).Inspect(ctx, newUpdater.PID); e != nil || actual != newUpdater {
		t.Fatal("stale takeover changed the replacement updater", e)
	}
	evidenceName := "external-takeover"
	if mutexHung {
		evidenceName = role + "-mutex-hung-takeover"
	}
	journalRaw, journalErr := os.ReadFile(boundedInstallerPath(cfg, auth.RequestID))
	if journalErr != nil {
		t.Fatal("read original external takeover journal", journalErr)
	}
	boundedFixtureEvidence(t, evidenceName, map[string]any{"unit": unit, "updater_config": configFile, "old_updater": oldUpdater, "new_updater": newUpdater, "retained_child_at_updater_exit": child, "unrelated_sentinel": sentinel, "lock_device": dev, "lock_inode": ino, "unresponsive_socket_error": rpcErr.Error(), "original_authorization": auth, "installer_result": result, "original_installer_journal": json.RawMessage(journalRaw), "mutex_held_and_sigterm_ignored": mutexHung})
	t.Logf("real systemd takeover: old=%+v new=%+v retained_child=%+v sentinel=%+v lock_device=%d lock_inode=%d", oldUpdater, newUpdater, child, sentinel, dev, ino)
}

type boundedLostStartProcess struct {
	LinuxProcess
	starts int
	actual Identity
}

func (p *boundedLostStartProcess) Start(ctx context.Context, binary string, cfg Config, env map[string]string) (Identity, error) {
	p.starts++
	id, err := p.LinuxProcess.Start(ctx, binary, cfg, env)
	if err != nil {
		return id, err
	}
	p.actual = id
	return Identity{}, ErrUnknown
}

func (p *boundedLostStartProcess) StartMaintenance(ctx context.Context, binary string, cfg Config, env map[string]string) (Identity, error) {
	return p.Start(ctx, binary, cfg, env)
}

func TestBoundedRealProcessLostStartReplyReconcilesOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sum, err := hashFile(self)
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, "helper.env")
	if err := os.WriteFile(envFile, []byte("TEND_BOUNDED_PROCESS_HELPER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{InstanceID: "bounded-lost-start", Role: "server", StateDir: root, InitialBinary: self, Args: []string{"-test.run=^TestBoundedSystemdProcessHelper$"}, EnvFiles: []string{envFile}, LogPath: filepath.Join(root, "child.log")}
	process := &boundedLostStartProcess{}
	s, err := Open(cfg, Options{Process: process})
	if err != nil {
		t.Fatal(err)
	}
	launch := "bounded-lost-start-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		// Race-instrumented helpers wait before exit; leave grace beyond that delay.
		// Reconcile even if a failing injection never observed Start's result.
		found, findErr := (LinuxProcess{}).Find(cleanup, launch, sum)
		if findErr != nil {
			t.Error("reconcile exact lost-start fixture during cleanup", findErr)
		}
		for _, id := range found {
			if e := (LinuxProcess{}).Stop(cleanup, id, 3*time.Second); e != nil {
				t.Error(e)
			}
		}
		_ = s.Close()
	})
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "bounded-lost-start", Successor: Binding{ProcessInstanceID: launch, ArtifactSHA256: sum}}
	target := Version{Binary: self, Artifact: release.Artifact{SHA256: sum}}
	s.state.Maintenance = &CommittedMaintenance{Plan: p, Target: target, Phase: "backed_up"}
	// A synthetic already-exited source isolates the real Start/Find boundary.
	s.state.Maintenance.Plan.FailedProcessExited = true
	s.state.Maintenance.AuthorizationBootID, s.state.Maintenance.AuthorizationDeadlineMillis, err = boundedFixtureDeadline()
	if err != nil {
		t.Fatal(err)
	}
	s.state.Maintenance.Plan.ExpiresAtMillis = time.Now().Add(time.Minute).UnixMilli()
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx); !errors.Is(err, ErrUnknown) {
		t.Fatalf("lost response must retain original unknown outcome: %v", err)
	}
	unknown := s.Status()
	if process.starts != 1 || unknown.Maintenance.Phase != "starting_successor" || unknown.Maintenance.Process.PID != 0 || !unknown.Maintenance.Irreversible {
		t.Fatal("unconfirmed start did not retain its original intent")
	}
	if err := s.Step(ctx); err != nil {
		t.Fatal("reconcile original launch", err)
	}
	resolved := s.Status()
	if process.starts != 1 || resolved.Maintenance.Phase != "checking_successor" || resolved.Maintenance.Process != process.actual {
		t.Fatal("lost start reply caused a second start or adopted a different process")
	}
	found, err := (LinuxProcess{}).Find(ctx, launch, sum)
	if err != nil || len(found) != 1 || found[0] != process.actual {
		t.Fatal("real native launch is not unique", err)
	}
	boundedFixtureEvidence(t, "lost-start-reply", map[string]any{"original_unknown_state": unknown, "reconciled_state": resolved, "actual_start_calls": process.starts, "native_process": process.actual, "native_matching_processes": found})
	t.Logf("original unknown phase=%s actual_starts=%d reconciled_exact_process=%+v", unknown.Maintenance.Phase, process.starts, process.actual)
}

func boundedFixtureDeadline() (string, int64, error) {
	boot, mono, err := BootClock()
	return boot, mono + time.Minute.Milliseconds(), err
}

func boundedSystemdRunnerGrant(t *testing.T, cfg Config, local CommittedHandoffPlan, source Identity, auth protocol.BoundedMaintenanceAuthorization) protocol.BoundedRunnerMaintenanceGrant {
	t.Helper()
	journal := cfg.DataFiles[0]
	if err := os.WriteFile(journal+".incarnation", []byte("isolated retained incarnation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	device, inode, err := BoundedDataFileIdentity(journal)
	if err != nil {
		t.Fatal(err)
	}
	incDevice, incInode, err := BoundedDataFileIdentity(journal + ".incarnation")
	if err != nil {
		t.Fatal(err)
	}
	data := protocol.BoundedDataIdentity{Path: journal, Device: device, Inode: inode, Incarnation: fmt.Sprintf("%d:%d", incDevice, incInode), Version: 1}
	body, err := os.ReadFile(cfg.InitialBinary)
	if err != nil {
		t.Fatal(err)
	}
	dir := makeBundle(t, body)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Storage.RunnerJournal = release.Range{Min: 1, Max: 17}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	bundle, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := bundle.Artifact("runner", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	target := Binding{UpgradeID: "bounded-runner-fixture", InstanceID: cfg.InstanceID, ProcessInstanceID: local.Maintenance.Successor.ProcessInstanceID, ReleaseID: bundle.Manifest.ReleaseID, ManifestSHA256: bundle.ManifestSHA256, ArtifactSHA256: artifact.SHA256}
	p := protocol.RunnerBoundedMaintenancePlan{HostID: cfg.InstanceID, NodeSelector: "isolated-fixture-selector", RunnerInstanceID: source.LaunchID, RunnerGeneration: 1, Source: source, Journal: data, ReleaseDir: dir, Target: target, ExpectedControlRevision: 1}
	sha, err := protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	auth.PlanSHA256, auth.Target, auth.DataIdentity = sha, target, &data
	auth.UpdaterTarget = &protocol.BoundedUpdaterTarget{Unit: local.Unit, UnitSHA256: local.UnitSHA256, ManagerSHA256: local.ManagerSHA256, UpdaterBinary: local.UpdaterBinary, Candidate: local.Candidate, CandidateSHA256: local.CandidateSHA256, ConfigFile: local.ConfigFile, ConfigFileSHA256: local.ConfigFileSHA256}
	p.Authorization = auth
	return protocol.BoundedRunnerMaintenanceGrant{RecoveryAuthorizationID: auth.RequestID, Plan: p, ControlRevision: 2, Runs: []protocol.CommittedRetainedRun{}, ProtectionSetSHA256: strings.Repeat("c", 64)}
}

func boundedFixtureFreeze(t *testing.T, ctx context.Context, id Identity) {
	t.Helper()
	fd, _, errno := syscall.Syscall(434, uintptr(id.PID), 0, 0) // pidfd_open.
	if errno != 0 {
		t.Fatal("pin exact fixture updater before freezing", errno)
	}
	defer syscall.Close(int(fd))
	if actual, err := (LinuxProcess{}).Inspect(ctx, id.PID); err != nil || actual != id {
		t.Fatal("fixture updater changed before freezing", err)
	}
	_, _, errno = syscall.Syscall6(424, fd, uintptr(syscall.SIGSTOP), 0, 0, 0, 0) // pidfd_send_signal.
	if errno != 0 {
		t.Fatal("freeze exact fixture updater", errno)
	}
}

func boundedFixtureEvidence(t *testing.T, name string, value any) {
	t.Helper()
	dir := os.Getenv("TEND_TEST_BOUNDED_EVIDENCE")
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("fixture evidence directory must be an explicit absolute path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("fixture evidence output already exists or is unreadable")
	}
	if err := writeJSON(path, value); err != nil {
		t.Fatal(err)
	}
}

func boundedRealHelper(t *testing.T, ctx context.Context, root, name string) Identity {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id, err := (LinuxProcess{}).Start(ctx, self, Config{Args: []string{"-test.run=^TestBoundedSystemdProcessHelper$"}, LogPath: filepath.Join(root, name+".log")}, map[string]string{"TEND_BOUNDED_PROCESS_HELPER": "1", "TEND_UPDATER_LAUNCH_ID": name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func boundedSystemdWait(t *testing.T, ctx context.Context, label string, fn func() bool) {
	t.Helper()
	for ctx.Err() == nil {
		if fn() {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("%s: %v", label, ctx.Err())
}
