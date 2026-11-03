//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// The exact old artifact is supplied privately. Historical state keeps its
// actual linked/starting_adopt shape; only this fixture's paths, manager and
// kernel identities are rebound. No production process or unit is accessed.
func TestCommittedHandoffActualLinkedUpdaterBootstrap(t *testing.T) {
	runCommittedHandoffFixture(t, false, false)
}
func TestCommittedHandoffActualLinkedUpdaterInterruptedBeforeStart(t *testing.T) {
	runCommittedHandoffFixture(t, true, false)
}
func TestCommittedHandoffActualFailedCandidateForward(t *testing.T) {
	runCommittedHandoffFixture(t, false, true)
}
func TestCommittedHandoffActualCancelledUpdaterReceiptRecovery(t *testing.T) {
	runCommittedHandoffFixture(t, false, false, true)
}
func TestCommittedHandoffActualEarlyExitForward(t *testing.T) {
	runCommittedHandoffFixture(t, false, true, false, true)
}
func runCommittedHandoffFixture(t *testing.T, interrupted, failed bool, recovery ...bool) {
	fast := len(recovery) > 1 && recovery[1]
	receiptRecovery := len(recovery) > 0 && recovery[0]
	if os.Getenv("TEND_TEST_SYSTEMD_USER") != "1" {
		t.Skip("opt-in isolated systemd fixture")
	}
	newServer := os.Getenv("TEND_TEST_NEW_SERVER")
	if failed {
		newServer = os.Getenv("TEND_TEST_BAD_SERVER")
		if fast {
			newServer = os.Getenv("TEND_TEST_FAST_SERVER")
		}
	}
	oldUpdater, newUpdater, oldServer, cli := os.Getenv("TEND_TEST_OLD_UPDATER"), os.Getenv("TEND_TEST_NEW_UPDATER"), os.Getenv("TEND_TEST_OLD_SERVER"), os.Getenv("TEND_TEST_TEND_CLI")
	for _, p := range []string{oldUpdater, newUpdater, oldServer, newServer, cli, os.Getenv("TEND_TEST_COMMITTED_STATE"), os.Getenv("TEND_TEST_COMMITTED_HANDOFF")} {
		if !filepath.IsAbs(p) {
			t.Fatal("explicit private artifact paths required")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "tend-cm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := filepath.Join(root, "updater")
	if err := copyAtomic(oldUpdater, fixed, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile, serverFile, socket := filepath.Join(root, "updater.json"), filepath.Join(root, "server.json"), filepath.Join(root, "server.sock")
	cfg := Config{InstanceID: "fixture-server", Role: "server", StateDir: stateDir, InitialBinary: oldServer, Args: []string{"--config", serverFile}, LogPath: filepath.Join(root, "server.log"), AuthoritySocket: socket, CoordinatorSocket: socket, AuthorizeURL: "http://localhost/updater/authorize", HealthURL: "http://localhost/updater/health", Protocol: 1, StorageVersion: 62, StopTimeoutSeconds: 3, HealthTimeoutSeconds: 10, DataFiles: []string{filepath.Join(root, "ledger.db")}}
	normalizeConfig(&cfg)
	if err := writeJSON(configFile, cfg); err != nil {
		t.Fatal(err)
	}
	serverConfig := map[string]any{"db": filepath.Join(root, "ledger.db"), "socket": socket, "updater": SocketPath(cfg), "releases": filepath.Join(root, "releases")}
	if receiptRecovery {
		serverConfig["receipt_recovery"] = true
		serverConfig["hosted_drained"] = true
	}
	if err := writeJSON(serverFile, serverConfig); err != nil {
		t.Fatal(err)
	}
	oldServerSHA, err := hashFile(oldServer)
	if err != nil {
		t.Fatal(err)
	}
	unit := "tend-committed-fixture-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".service"
	var child Identity
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = (systemdUser{}).call(cleanup, "stop", unit)
		_ = (LinuxProcess{}).Stop(cleanup, child, 3*time.Second)
		if st, e := readState(filepath.Join(stateDir, "state.json")); e == nil && st.Maintenance != nil && st.Maintenance.Process.PID > 1 {
			_ = (LinuxProcess{}).Stop(cleanup, st.Maintenance.Process, 3*time.Second)
		}
		_, _ = (systemdUser{}).call(cleanup, "reset-failed", unit)
	})
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(configDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unitFile := filepath.Join(unitDir, unit)
	unitFD, err := os.OpenFile(unitFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := unitFD.WriteString("[Unit]\nDescription=Isolated committed maintenance fixture\n[Service]\nType=simple\nKillMode=process\nRestart=on-failure\nTimeoutStopSec=4s\nExecStart=" + fixed + " serve --config " + configFile + "\nStandardOutput=append:" + filepath.Join(root, "updater.log") + "\nStandardError=append:" + filepath.Join(root, "updater.log") + "\n")
	closeErr := unitFD.Close()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = (systemdUser{}).call(cleanup, "stop", unit)
		_ = os.Remove(unitFile)
		_, _ = (systemdUser{}).call(cleanup, "daemon-reload")
	})
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, err := (systemdUser{}).call(ctx, "daemon-reload"); err != nil {
		t.Fatal(err)
	}
	var actual State
	raw, err := os.ReadFile(os.Getenv("TEND_TEST_COMMITTED_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := strictJSON(raw, &actual); err != nil {
		t.Fatal(err)
	}
	var previous HandoffJournal
	raw, err = os.ReadFile(os.Getenv("TEND_TEST_COMMITTED_HANDOFF"))
	if err != nil {
		t.Fatal(err)
	}
	if err := strictJSON(raw, &previous); err != nil {
		t.Fatal(err)
	}
	if actual.Format != 2 || actual.Operation != nil || actual.Recovery == nil || actual.Recovery.Phase != "ledger_linked" || previous.Phase != "starting_adopt" {
		t.Fatal("actual fixture must retain linked state and original starting_adopt journal")
	}
	info, _ := os.Stat(oldServer)
	version := actual.Current
	version.Binary = oldServer
	version.Artifact = release.Artifact{Role: "server", OS: "linux", Arch: runtime.GOARCH, Path: "server", SHA256: oldServerSHA, Size: info.Size()}
	previous.Plan.Unit = unit
	previous.Plan.ConfigFile = configFile
	previous.Plan.UpdaterBinary = fixed
	previous.Plan.CandidateSHA256, _ = hashFile(fixed)
	previous.Plan.Recovery.ConfigSHA256 = digest(cfg)
	// Create the original inode before starting the old binary, so its original
	// startup guard can validate the relocated historical handoff.
	lock, err := lockDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	previous.Plan.LockDevice, previous.Plan.LockInode, _ = lockIdentity(stateDir)
	start := exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "start", unit)
	// A paused unit permits binding its exact policy without an unreviewed old
	// startup racing the fixture's initial historical journal.
	if err := writeJSON(filepath.Join(stateDir, "state.json"), State{Format: 1, InstanceID: cfg.InstanceID, Current: version}); err != nil {
		t.Fatal(err)
	}
	if raw, err := start.CombinedOutput(); err != nil {
		t.Fatalf("fixture unit start: %v %s", err, raw)
	}
	wait := func(label string, fn func() bool) {
		t.Helper()
		for ctx.Err() == nil {
			if fn() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		log, _ := os.ReadFile(cfg.LogPath)
		updaterLog, _ := os.ReadFile(filepath.Join(root, "updater.log"))
		t.Fatalf("%s: %v; fixture server log: %s; fixture updater log: %s", label, ctx.Err(), log, updaterLog)
	}
	wait("old updater child startup", func() bool {
		st, e := Call(ctx, cfg, "status", ControlRequest{})
		if e != nil || st.Process.PID < 2 || st.Operation == nil || st.Operation.Phase != "committed" {
			return false
		}
		child = st.Process
		return true
	})
	binding := actual.CommittedProof.Binding
	binding.InstanceID = cfg.InstanceID
	binding.ProcessInstanceID = child.LaunchID
	binding.ArtifactSHA256 = child.SHA256
	actual.InstanceID = cfg.InstanceID
	actual.Process = child
	actual.Current = version
	actual.CommittedProof = &CommittedProof{Binding: binding, Version: version, Process: child}
	actual.Recovery.Plan.ConfigSHA256 = digest(cfg)
	actual.Recovery.Plan.Successor = binding
	actual.Recovery.Process = child
	actual.Recovery.Target = version
	previous.Plan.Recovery = actual.Recovery.Plan

	_, previous.Plan.ManagerSHA256, err = managerSnapshot(ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	previous.Plan.UnitSHA256, err = unitDigest(ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (systemdUser{}).call(ctx, "stop", unit); err != nil {
		t.Fatal(err)
	}
	if live, err := (LinuxProcess{}).Inspect(ctx, child.PID); err != nil || live != child {
		t.Fatal("actual old updater did not retain child", err)
	}
	if err := writeJSON(filepath.Join(stateDir, "state.json"), actual); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(handoffPath(cfg), previous); err != nil {
		t.Fatal(err)
	}
	parentBytes, _ := os.ReadFile(handoffPath(cfg))
	recoveryBefore, _ := json.Marshal(actual.Recovery)
	if _, err := (systemdUser{}).call(ctx, "start", unit); err != nil {
		t.Fatal(err)
	}
	wait("actual old linked updater startup", func() bool {
		st, e := Call(ctx, cfg, "status", ControlRequest{})
		return e == nil && st.Format == 2 && st.Recovery != nil && st.Recovery.Phase == "ledger_linked"
	})
	newServerBytes, e := os.ReadFile(newServer)
	if e != nil {
		t.Fatal(e)
	}
	dir := makeBundle(t, newServerBytes)
	manifestRaw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var manifest release.Manifest
	_ = json.Unmarshal(manifestRaw, &manifest)
	manifest.Artifacts[0].Role = "server"
	manifest.Protocol = release.Range{Min: 1, Max: 2}
	manifest.Storage.ServerSchema = release.Range{Min: 62, Max: 82}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(5 * time.Minute).UnixMilli()
	resourceFile := filepath.Join(root, "resources.json")
	if err := writeJSON(resourceFile, ResourceHandoff{WindowClaim: "fixture-committed-window", OperatorUID: os.Getuid(), ExpiresAtMillis: expires, RunnerCoverageUntilMillis: expires, WriterProcesses: []Identity{{PID: 2147483647, StartTicks: 1, LaunchID: "fixture-gone"}}, PendingOperationsReconciled: "fixture installer exclusively owned", WorkspaceResponsibility: "retained original child and run facts", Source: "isolated fixture"}); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(root, "exit-proof.json")
	oldSHA, _ := hashFile(fixed)
	if err := writeJSON(evidence, UpdaterExitEvidence{OperatorUID: os.Getuid(), ArtifactSHA256: oldSHA, Source: "exact supplied committed updater artifact", ReviewArtifact: "fixture observed exact child retained after stop", ExitPolicy: "retains-managed-child", ExpiresAtMillis: expires}); err != nil {
		t.Fatal(err)
	}
	resourceRaw, _ := os.ReadFile(resourceFile)
	evidenceRaw, _ := os.ReadFile(evidence)
	p := CommittedHandoffPlan{Unit: unit, UpdaterBinary: fixed, ConfigFile: configFile, Candidate: newUpdater, OldUpdaterEvidence: evidence, OldUpdaterEvidenceSHA256: bytesDigest(evidenceRaw), Maintenance: protocol.CommittedMaintenancePlan{MaintenanceID: "fixture-committed-window", PreparationRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEX", PreparationCancelRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEV", LinkRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEW", ReleaseDir: dir, Successor: Binding{ProcessInstanceID: unit + "-successor"}, OperatorUID: os.Getuid(), WindowClaim: "fixture-committed-window", ExpiresAtMillis: expires, ResourceEvidence: resourceFile, ResourceSHA256: bytesDigest(resourceRaw), PreparationRevision: 1}}
	if receiptRecovery {
		cancelledUpdater := os.Getenv("TEND_TEST_CANCELLED_UPDATER")
		if !filepath.IsAbs(cancelledUpdater) {
			t.Fatal("exact prior cancelled-scope updater artifact is required")
		}
		prior := p
		prior.Candidate = cancelledUpdater
		prior.Maintenance.MaintenanceID = "fixture-prior-cancelled"
		prior.Maintenance.Successor.ProcessInstanceID = unit + "-prior-cancelled"
		prior, err = PrepareCommittedHandoffPlan(ctx, cfg, prior)
		if err != nil {
			t.Fatal("prepare actual previous updater hop", err)
		}
		priorFile := filepath.Join(root, "prior-cancelled-plan.json")
		if err := writeJSON(priorFile, prior); err != nil {
			t.Fatal(err)
		}
		sum, _ := hashFile(cancelledUpdater)
		command := exec.CommandContext(ctx, cli, "updater", "maintenance", "apply", "--installer", cancelledUpdater, "--installer-sha256", sum, "--config", configFile, "--plan", priorFile, "--json")
		_, _ = command.CombinedOutput()
		wait("actual previous updater adoption", func() bool {
			st, e := Call(ctx, cfg, "status", ControlRequest{})
			return e == nil && st.Format == 3 && st.Maintenance != nil && st.Maintenance.Phase == "updater_adopted"
		})
		if err := callMaintenanceFixtureCLI(ctx, cli, prior, priorFile, cfg, "prepare"); err != nil {
			t.Fatal(err)
		}
		if err := callMaintenanceFixtureCLI(ctx, cli, prior, priorFile, cfg, "cancel"); err != nil {
			t.Fatal(err)
		}
		priorState, e := Call(ctx, cfg, "status", ControlRequest{})
		if e != nil || priorState.Maintenance.Phase != "cancelled" || priorState.Process != child || priorState.Current != actual.Current {
			t.Fatal("prior cancellation changed committed child", e)
		}
		t.Logf("actual previous updater reached Format3 cancelled scope with original child retained")
		// The new immutable root starts from the genuine prior cancellation;
		// neither its journal nor its committed state is hand-rewritten.
		for field, value := range map[*string]string{&p.Maintenance.RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF5", &p.Maintenance.LinkRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF6", &p.Maintenance.PreparationRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF7", &p.Maintenance.PreparationCancelRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF8"} {
			*field = value
		}
		p.Maintenance.PreparationRevision = 3
		p.Maintenance.ReceiptRecoveryStop = &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{}}
		if err := writeJSON(evidence, UpdaterExitEvidence{OperatorUID: os.Getuid(), ArtifactSHA256: sum, Source: "exact supplied previous cancelled-scope updater", ReviewArtifact: "actual isolated adoption and safe cancellation", ExitPolicy: "retains-managed-child", ExpiresAtMillis: expires}); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(evidence)
		p.OldUpdaterEvidenceSHA256 = bytesDigest(raw)
	}
	if os.Getenv("TEND_TEST_FLOCK_BTRFS") == "1" {
		dev, ino, e := lockIdentity(stateDir)
		if e != nil {
			t.Fatal(e)
		}
		owner, known := flockOwner(dev, ino)
		if !known || owner != 0 {
			t.Fatalf("expected legacy device lookup to miss Btrfs subvolume lock, owner=%d known=%v", owner, known)
		}
		t.Logf("real Btrfs subvolume legacy ownership lookup rejected: device=%d inode=%d", dev, ino)
	}
	p, err = PrepareCommittedHandoffPlan(ctx, cfg, p)
	if err != nil {
		t.Fatal("new installer cannot plan from actual linked shape", err)
	}
	planFile := filepath.Join(root, "plan.json")
	if err := writeJSON(planFile, p); err != nil {
		t.Fatal(err)
	}
	var continuation []string
	if interrupted {
		injected := errors.New("fixture interrupted before starting reviewed updater")
		j, e := applyCommittedHandoff(ctx, cfg, p, func() error { return injected })
		if !errors.Is(e, injected) || j.Phase != "starting_adopt" {
			t.Fatalf("interruption did not preserve expected intent: %s %v", j.Phase, e)
		}
		if live, e := (LinuxProcess{}).Inspect(ctx, child.PID); e != nil || live != child {
			t.Fatal("interruption lost retained child", e)
		}
		continuation = []string{"--resume-request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEZ", "--expected-generation", strconv.FormatUint(j.Generation, 10)}
	}
	newSHA, _ := hashFile(newUpdater)
	apply := exec.CommandContext(ctx, cli, "updater", "maintenance", "apply", "--installer", newUpdater, "--installer-sha256", newSHA, "--config", configFile, "--plan", planFile, "--json")
	apply.Args = append(apply.Args, continuation...)
	applyRaw, applyErr := apply.CombinedOutput()
	t.Logf("first-hop command exit=%v", applyErr)
	wait("new independent updater adoption", func() bool {
		st, e := Call(ctx, cfg, "status", ControlRequest{})
		if e != nil || st.Maintenance == nil {
			return false
		}
		return st.Format == 3 && st.Maintenance.Phase == "updater_adopted"
	})
	st, err := Call(ctx, cfg, "status", ControlRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Process != child || st.Current != version || !reflect.DeepEqual(st.CommittedProof, actual.CommittedProof) {
		t.Fatal("updater-only bootstrap replaced the committed server", string(applyRaw))
	}
	recoveryAfter, _ := json.Marshal(st.Recovery)
	parentAfter, _ := os.ReadFile(handoffPath(cfg))
	if string(recoveryBefore) != string(recoveryAfter) || string(parentBytes) != string(parentAfter) {
		t.Fatal("bootstrap rewrote old forward-recovery history")
	}
	if live, err := (LinuxProcess{}).Inspect(ctx, child.PID); err != nil || live != child {
		t.Fatal("original child lost across first hop", err)
	}
	manager, _, lockErr := managerSnapshot(ctx, unit)
	owner, parseErr := strconv.Atoi(manager["MainPID"])
	ownsLock, lockErr2 := processOwnsFlock(ctx, "/proc", owner, p.LockDevice, p.LockInode)
	if lockErr != nil || parseErr != nil || lockErr2 != nil || owner == p.Updater.PID || !ownsLock {
		t.Fatal("new updater did not acquire original flock", lockErr, parseErr, lockErr2)
	}
	// Preparing admission uses the existing old-server API. Its retained run
	// facts are neither cancelled nor rewritten to claim a normal upgrade drain.
	if err := callMaintenanceFixtureCLI(ctx, cli, p, planFile, cfg, "prepare"); err != nil {
		t.Fatal(err)
	}
	if receiptRecovery {
		err = callReceiptRecoveryFixtureCLI(ctx, cli, p, planFile, cfg, "req_01M6F01JSS6Q9BFV6DJNSSBHF9")
	} else {
		err = callMaintenanceFixtureCLI(ctx, cli, p, planFile, cfg, "authorize")
	}
	if err != nil {
		t.Fatal(err)
	}
	var committed State
	var ledgerRaw []byte
	if !failed {
		wait("successor independent committed ledger link", func() bool {
			st, e := Call(ctx, cfg, "status", ControlRequest{})
			return e == nil && st.Maintenance != nil && st.Maintenance.Phase == "ledger_linked"
		})
		committed, e = Call(ctx, cfg, "status", ControlRequest{})
		if e != nil {
			t.Fatal(e)
		}
		if committed.Maintenance.Receipt == nil || committed.Maintenance.HealthCount < 2 || committed.Maintenance.Receipt.SecondSequence <= committed.Maintenance.Receipt.FirstSequence || committed.Process == child || committed.CommittedProof == nil || committed.CommittedProof.Binding != p.Maintenance.Successor {
			t.Fatal("missing actual successor proof")
		}
		ledger := exec.CommandContext(ctx, cli, "updater", "maintenance", "ledger", "--socket", socket, "--maintenance-id", p.Maintenance.MaintenanceID, "--json")
		ledgerRaw, e = ledger.CombinedOutput()
		if e != nil {
			t.Fatalf("independent ledger read: %v %s", e, ledgerRaw)
		}
		var row protocol.CommittedMaintenanceLedger
		if e := json.Unmarshal(ledgerRaw, &row); e != nil || !row.Linked || row.Receipt == nil || *row.Receipt != *committed.Maintenance.Receipt {
			t.Fatalf("independent database link missing: %v %s", e, ledgerRaw)
		}
		if err := callMaintenanceFixtureCLI(ctx, cli, p, planFile, cfg, "finish"); err != nil {
			t.Fatal(err)
		}
		parentAfter, _ = os.ReadFile(handoffPath(cfg))
		recoveryAfter, _ = json.Marshal(committed.Recovery)
		if string(parentBytes) != string(parentAfter) || string(recoveryBefore) != string(recoveryAfter) {
			t.Fatal("server replacement changed previous recovery history")
		}
		t.Logf("actual successor committed and independently linked; generations=%d health=%d", committed.Generation, committed.Maintenance.HealthCount)

	} else {
		wait("failed candidate checking without API", func() bool {
			st, e := Call(ctx, cfg, "status", ControlRequest{})
			return e == nil && st.Maintenance != nil && ((st.Maintenance.Phase == "checking_successor" && st.Maintenance.LastError != "") || (fast && st.Maintenance.Phase == "launch_exited" && st.Maintenance.LastError == "updater: owned candidate exited; independent failed-parent forward required"))
		})
		committed, e = Call(ctx, cfg, "status", ControlRequest{})
		if e != nil {
			t.Fatal(e)
		}
		if committed.Maintenance.Receipt != nil || committed.Maintenance.HealthCount != 0 || committed.Current != actual.Current {
			t.Fatal("failed candidate was represented as committed")
		}
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			t.Fatal("failed candidate unexpectedly exposed a control API")
		}
		if fast {
			if err := ValidateMaintenanceLaunchExit(*committed.Maintenance); err != nil {
				t.Fatal("real early exit lacked owned wait proof", err)
			}
			t.Logf("owned Wait proves real fast candidate exited before identity: %+v", committed.Maintenance.LaunchExit)
		} else {
			t.Log("actual failed candidate remains alive with no service API; original proof remains historical")
		}
	}
	firstHead, e := os.ReadFile(committedHeadPath(cfg))
	if e != nil {
		t.Fatal(e)
	}
	firstMaintenance := *committed.Maintenance
	secondServer := os.Getenv("TEND_TEST_SECOND_SERVER")
	secondBytes, e := os.ReadFile(secondServer)
	if e != nil {
		t.Fatal(e)
	}
	secondDir := makeBundle(t, secondBytes)
	secondRaw, _ := os.ReadFile(filepath.Join(secondDir, "manifest.json"))
	var secondManifest release.Manifest
	if e := json.Unmarshal(secondRaw, &secondManifest); e != nil {
		t.Fatal(e)
	}
	secondManifest.Artifacts[0].Role = "server"
	secondManifest.Protocol = release.Range{Min: 1, Max: 2}
	secondManifest.Storage.ServerSchema = release.Range{Min: 62, Max: 82}
	if e := writeJSON(filepath.Join(secondDir, "manifest.json"), secondManifest); e != nil {
		t.Fatal(e)
	}
	secondResource := filepath.Join(root, "second-resource.json")
	resourceRaw, _ = os.ReadFile(resourceFile)
	var resource ResourceHandoff
	_ = json.Unmarshal(resourceRaw, &resource)
	resource.WindowClaim = "fixture-second-window"
	if e := writeJSON(secondResource, resource); e != nil {
		t.Fatal(e)
	}
	resourceRaw, _ = os.ReadFile(secondResource)
	secondEvidence := filepath.Join(root, "second-updater-exit.json")
	fixedSHA, _ := hashFile(fixed)
	if e := writeJSON(secondEvidence, UpdaterExitEvidence{OperatorUID: os.Getuid(), ArtifactSHA256: fixedSHA, Source: "isolated just-adopted reviewed updater", ReviewArtifact: "actual first-hop retained child and same fixed manager", ExitPolicy: "retains-managed-child", ExpiresAtMillis: expires}); e != nil {
		t.Fatal(e)
	}
	secondEvidenceRaw, _ := os.ReadFile(secondEvidence)
	next := p
	next.Updater = Identity{}
	next.ParentCommittedHeadSHA256 = ""
	next.OldUpdaterEvidence = secondEvidence
	next.OldUpdaterEvidenceSHA256 = bytesDigest(secondEvidenceRaw)
	next.Maintenance = protocol.CommittedMaintenancePlan{MaintenanceID: "fixture-second-window", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", LinkRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF2", PreparationRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF3", PreparationCancelRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF4", ReleaseDir: secondDir, Successor: Binding{ProcessInstanceID: unit + "-second-successor"}, OperatorUID: os.Getuid(), WindowClaim: resource.WindowClaim, ResourceEvidence: secondResource, ResourceSHA256: bytesDigest(resourceRaw), ExpiresAtMillis: expires, PreparationRevision: 3}
	if receiptRecovery {
		next.Maintenance.ReceiptRecoveryStop = p.Maintenance.ReceiptRecoveryStop
		next.Maintenance.PreparationRevision = 5
	}
	if failed {
		next.Maintenance.ParentMaintenanceID = p.Maintenance.MaintenanceID
		next.Maintenance.FailedProcessExited = fast
		next.Maintenance.PreparationRevision = p.Maintenance.PreparationRevision
		next.Maintenance.PreparationRequestID = p.Maintenance.PreparationRequestID
		next.Maintenance.PreparationCancelRequestID = p.Maintenance.PreparationCancelRequestID
		next.Maintenance.HostControls = p.Maintenance.HostControls
	}
	next, e = PrepareCommittedHandoffPlan(ctx, cfg, next)
	if e != nil {
		t.Fatal("next committed deployment cannot plan", e)
	}
	nextFile := filepath.Join(root, "second-plan.json")
	if e := writeJSON(nextFile, next); e != nil {
		t.Fatal(e)
	}
	secondApply := exec.CommandContext(ctx, cli, "updater", "maintenance", "apply", "--installer", newUpdater, "--installer-sha256", newSHA, "--config", configFile, "--plan", nextFile, "--json")
	_, secondApplyErr := secondApply.CombinedOutput()
	t.Logf("second first-hop command exit=%v", secondApplyErr)
	wait("second independent updater window adopted", func() bool {
		st, e := Call(ctx, cfg, "status", ControlRequest{})
		return e == nil && st.Maintenance != nil && st.Maintenance.Plan.MaintenanceID == next.Maintenance.MaintenanceID && st.Maintenance.Phase == "updater_adopted"
	})
	if e := callMaintenanceFixtureCLI(ctx, cli, next, nextFile, cfg, "prepare"); e != nil {
		t.Fatal(e)
	}
	if receiptRecovery {
		e = callReceiptRecoveryFixtureCLI(ctx, cli, next, nextFile, cfg, "req_01M6F01JSS6Q9BFV6DJNSSBHFA")
	} else {
		e = callMaintenanceFixtureCLI(ctx, cli, next, nextFile, cfg, "authorize")
	}
	if e != nil {
		t.Fatal(e)
	}
	wait("second successor independently linked", func() bool {
		st, e := Call(ctx, cfg, "status", ControlRequest{})
		return e == nil && st.Maintenance != nil && st.Maintenance.Plan.MaintenanceID == next.Maintenance.MaintenanceID && st.Maintenance.Phase == "ledger_linked"
	})
	final, e := Call(ctx, cfg, "status", ControlRequest{})
	if e != nil {
		t.Fatal(e)
	}
	historical, ok := final.MaintenanceHistory[p.Maintenance.MaintenanceID]
	if !ok || !reflect.DeepEqual(historical, firstMaintenance) {
		t.Fatal("second window changed prior maintenance history")
	}
	j, e := readCommittedJournal(cfg)
	if e != nil || string(j.PreviousHead) != string(firstHead) || next.ParentCommittedHeadSHA256 != bytesDigest(firstHead) {
		t.Fatal("second immutable journal chain broken", e)
	}
	if !failed {
		ledgerAgain := exec.CommandContext(ctx, cli, "updater", "maintenance", "ledger", "--socket", socket, "--maintenance-id", p.Maintenance.MaintenanceID, "--json")
		oldLedger, e := ledgerAgain.CombinedOutput()
		if e != nil || string(oldLedger) != string(ledgerRaw) {
			t.Fatal("second deployment changed original independent ledger", e, string(oldLedger))
		}
	}
	if e := callMaintenanceFixtureCLI(ctx, cli, next, nextFile, cfg, "finish"); e != nil {
		t.Fatal(e)
	}
	t.Logf("second independent maintenance committed and linked; generation=%d", final.Generation)

}

func callMaintenanceFixtureCLI(ctx context.Context, cli string, p CommittedHandoffPlan, planFile string, cfg Config, action string) error {
	st, err := Call(ctx, cfg, "status", ControlRequest{})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, cli, "updater", "maintenance", action, "--socket", SocketPath(cfg), "--plan", planFile, "--expected-generation", strconv.FormatUint(st.Generation, 10), "--json")
	if raw, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fixture maintenance %s: %w %s", action, err, raw)
	}
	return nil
}

func callReceiptRecoveryFixtureCLI(ctx context.Context, cli string, p CommittedHandoffPlan, planFile string, cfg Config, request string) error {
	st, err := Call(ctx, cfg, "status", ControlRequest{})
	if err != nil {
		return err
	}
	first, second := planFile+".capture1.json", planFile+".capture2.json"
	for _, path := range []string{first, second} {
		if err := writeJSON(path, p.Maintenance.ReceiptRecoveryStop.Runs); err != nil {
			return err
		}
	}
	command := exec.CommandContext(ctx, cli, "updater", "maintenance", "recover-stop", "--socket", SocketPath(cfg), "--plan", planFile, "--expected-generation", strconv.FormatUint(st.Generation, 10), "--request-id", request, "--first-capture", first, "--second-capture", second, "--json")
	raw, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("explicit receipt recovery command: %w: %s", err, raw)
	}
	return nil
}
