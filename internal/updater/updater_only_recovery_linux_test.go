//go:build linux

package updater

import (
	"context"
	"errors"
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

func updaterOnlyLinkedFixture(t *testing.T) (*harness, CommittedHandoffPlan, protocol.BoundedMaintenanceAuthorization) {
	t.Helper()
	h, p, a := committedFixture(t)
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.state.Generation); err != nil {
		t.Fatal(err)
	}
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: h.s.state.Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "bounded fixture", ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	a.archive.HostedDrained = false
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	st := h.s.Status()
	if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
		t.Fatal(err)
	}
	auth.ExpectedGeneration, auth.ExpectedPhase = h.s.state.Generation, "ledger_linked"
	plan := CommittedHandoffPlan{Maintenance: p, Unit: "fixture.service", UnitSHA256: strings.Repeat("a", 64), ManagerSHA256: strings.Repeat("b", 64), UpdaterBinary: "/fixture/fixed", Candidate: "/fixture/old", CandidateSHA256: strings.Repeat("c", 64), ConfigFile: "/fixture/config", ConfigFileSHA256: strings.Repeat("d", 64)}
	auth.UpdaterTarget = &protocol.BoundedUpdaterTarget{Unit: plan.Unit, UnitSHA256: plan.UnitSHA256, ManagerSHA256: plan.ManagerSHA256, UpdaterBinary: plan.UpdaterBinary, Candidate: "/fixture/new", CandidateSHA256: strings.Repeat("e", 64), ConfigFile: plan.ConfigFile, ConfigFileSHA256: plan.ConfigFileSHA256}
	return h, plan, auth
}

func TestUpdaterOnlyRecoveryRequiresExactCommittedWriter(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	if err := validateUpdaterOnlyState(h.s.Status(), p, a); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*State){
		"current process":      func(s *State) { s.Process.StartTicks++ },
		"maintenance process":  func(s *State) { s.Maintenance.Process.StartTicks++ },
		"proof process":        func(s *State) { s.CommittedProof.Process.StartTicks++ },
		"receipt process":      func(s *State) { s.Maintenance.Receipt.Process.StartTicks++ },
		"receipt binding":      func(s *State) { s.Maintenance.Receipt.Successor.ProcessInstanceID = "foreign" },
		"receipt sequences":    func(s *State) { s.Maintenance.Receipt.SecondSequence = s.Maintenance.Receipt.FirstSequence },
		"receipt epoch":        func(s *State) { s.Maintenance.Receipt.Epoch = "foreign" },
		"current artifact":     func(s *State) { s.Current.Artifact.SHA256 = strings.Repeat("f", 64) },
		"proof version":        func(s *State) { s.CommittedProof.Version.StorageVersion++ },
		"target version":       func(s *State) { s.Maintenance.Target.StorageVersion++ },
		"irreversible":         func(s *State) { s.Maintenance.Irreversible = false },
		"missing receipt":      func(s *State) { s.Maintenance.Receipt = nil },
		"missing exit":         func(s *State) { s.Bounded.SourceExit = nil },
		"wrong exited process": func(s *State) { s.Bounded.SourceExit.Process = s.Process },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			st := h.s.Status()
			change(&st)
			if err := validateUpdaterOnlyState(st, p, a); err == nil {
				t.Fatal("accepted changed committed writer")
			}
		})
	}
	if h.p.stops != 1 {
		t.Fatal("pure validation changed native service effects")
	}
}

func TestUpdaterOnlyRecoveryTargetRetainsFixedConfiguration(t *testing.T) {
	_, p, a := updaterOnlyLinkedFixture(t)
	if err := validateUpdaterOnlyTarget(p, a); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"unit", "policy", "binary", "config", "same candidate", "missing"} {
		t.Run(field, func(t *testing.T) {
			copy := a
			target := *a.UpdaterTarget
			copy.UpdaterTarget = &target
			switch field {
			case "unit":
				target.Unit = "foreign.service"
			case "policy":
				target.ManagerSHA256 = strings.Repeat("f", 64)
			case "binary":
				target.UpdaterBinary = "/foreign"
			case "config":
				target.ConfigFile = "/foreign"
			case "same candidate":
				target.CandidateSHA256 = p.CandidateSHA256
			case "missing":
				copy.UpdaterTarget = nil
			}
			if err := validateUpdaterOnlyTarget(p, copy); err == nil {
				t.Fatal("accepted changed updater-only scope")
			}
		})
	}
}

func TestUpdaterOnlyRecoveryReplayOnlyObservesOriginalAttempt(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	a.UpdaterSource = &Identity{PID: 123, StartTicks: 456, LaunchID: "fixture-controller", SHA256: strings.Repeat("a", 64)}
	a.UpdaterLock = &protocol.BoundedDataIdentity{Device: 1, Inode: 2}
	path := updaterOnlyJournalPath(h.cfg, a.RequestID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	j := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: a, ConfigSHA256: digest(h.cfg)}, Phase: "starting_updater"}
	if err := writeJSON(path, j); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	result, err := takeoverCommittedUpdaterOnly(t.Context(), h.cfg, p, a)
	if err != nil {
		t.Fatal(err)
	}
	if result.(updaterOnlyJournal).Phase != "starting_updater" || digest(before) != digest(h.s.Status()) {
		t.Fatal("lost reply replay performed new effects")
	}
	a.Reason = "changed payload"
	if _, err := takeoverCommittedUpdaterOnly(t.Context(), h.cfg, p, a); err == nil {
		t.Fatal("accepted conflicting request")
	}
}

func TestUpdaterOnlyRecoveryPreStartFreshCAS(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	a.UpdaterSource = &Identity{PID: 123, StartTicks: 456, LaunchID: "fixture-controller", SHA256: strings.Repeat("a", 64)}
	a.UpdaterLock = &protocol.BoundedDataIdentity{Device: 1, Inode: 2}
	if err := os.MkdirAll(filepath.Dir(committedJournalPath(h.cfg, p.Maintenance.MaintenanceID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedJournalPath(h.cfg, p.Maintenance.MaintenanceID), CommittedHandoffJournal{Plan: p, Phase: "starting_adopt"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedHeadPath(h.cfg), committedHandoffHead{MaintenanceID: p.Maintenance.MaintenanceID, PlanSHA256: digest(p)}); err != nil {
		t.Fatal(err)
	}
	headSHA, journalSHA, err := updaterOnlyOriginalFiles(h.cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	st := h.s.Status()
	j := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: a, ConfigSHA256: digest(h.cfg), OriginalHeadSHA256: headSHA, OriginalJournalSHA256: journalSHA, OriginalState: st, Retained: st.Process}, Phase: "installing_updater"}
	if err := os.MkdirAll(filepath.Dir(updaterOnlyJournalPath(h.cfg, a.RequestID)), 0o700); err != nil {
		t.Fatal(err)
	}
	publish := func() {
		t.Helper()
		if err := writeJSON(updaterOnlyJournalPath(h.cfg, a.RequestID), j); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(updaterOnlyHeadPath(h.cfg), updaterOnlyHead{Format: 1, RequestID: a.RequestID, ScopeSHA256: digest(j.Scope)}); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	fresh := a
	fresh.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
	idle := map[string]string{"MainPID": "0", "ActiveState": "inactive", "SubState": "dead", "Job": "0"}
	if err := validateUpdaterOnlyPreStartRecovery(h.cfg, p, fresh, st, idle); err != nil {
		t.Fatal(err)
	}
	failed := map[string]string{"MainPID": "0", "ActiveState": "failed", "SubState": "failed", "Job": "0"}
	if err := validateUpdaterOnlyPreStartRecovery(h.cfg, p, fresh, st, failed); err != nil {
		t.Fatal("exact source exit may leave a failed idle manager", err)
	}

	for _, phase := range []string{"starting_updater", "adopted"} {
		j.Phase = phase
		publish()
		if err := validateUpdaterOnlyPreStartRecovery(h.cfg, p, fresh, st, idle); err == nil {
			t.Fatalf("fresh authority retried %s", phase)
		}
	}
	j.Phase = "planned"
	publish()
	if err := validateUpdaterOnlyPreStartRecovery(h.cfg, p, fresh, st, idle); err != nil {
		t.Fatal("planned interruption should remain recoverable", err)
	}
	j.Phase = "installing_updater"
	publish()
	for _, job := range []string{"18", "unknown"} {
		manager := map[string]string{"MainPID": "0", "ActiveState": "inactive", "SubState": "dead", "Job": job}
		if err := validateUpdaterOnlyPreStartRecovery(h.cfg, p, fresh, st, manager); err == nil {
			t.Fatal("accepted pending manager work")
		}
	}
	changed := cloneState(st)
	changed.Generation++
	if err := validateUpdaterOnlyPreStartRecovery(h.cfg, p, fresh, changed, idle); err == nil {
		t.Fatal("accepted changed source state")
	}
	if err := os.WriteFile(handoffPath(h.cfg), []byte("changed parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := updaterOnlyOriginalFiles(h.cfg, p); err == nil {
		t.Fatal("accepted changed legacy parent")
	}
}

func TestUpdaterOnlyRecoveryRequiresIndependentRequest(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	st := h.s.Status()
	for _, id := range []string{p.Maintenance.RequestID, p.Maintenance.LinkRequestID, p.Maintenance.PreparationRequestID, p.Maintenance.PreparationCancelRequestID, a.RequestID, p.Maintenance.HostControls[0].RequestID, p.Maintenance.HostControls[0].RestoreRequestID} {
		if err := validateUpdaterOnlyFreshRequest(st, id); err == nil {
			t.Fatal("accepted request already used by source maintenance")
		}
	}
	fresh := "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
	if err := validateUpdaterOnlyFreshRequest(st, fresh); err != nil {
		t.Fatal(err)
	}
	st.BoundedHistory = map[string]protocol.BoundedMaintenanceState{fresh: {}}
	if err := validateUpdaterOnlyFreshRequest(st, fresh); err == nil {
		t.Fatal("accepted archived bounded request")
	}
}

func TestUpdaterOnlyRecoveryPendingAttemptPreservesAdoptedStartup(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	cfg := h.s.cfg
	if err := os.MkdirAll(filepath.Dir(committedJournalPath(cfg, p.Maintenance.MaintenanceID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedJournalPath(cfg, p.Maintenance.MaintenanceID), CommittedHandoffJournal{Plan: p, Phase: "starting_adopt"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedHeadPath(cfg), committedHandoffHead{MaintenanceID: p.Maintenance.MaintenanceID, PlanSHA256: digest(p)}); err != nil {
		t.Fatal(err)
	}
	headSHA, journalSHA, err := updaterOnlyOriginalFiles(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	a.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
	a.UpdaterSource = &Identity{PID: 123, StartTicks: 456, SHA256: p.CandidateSHA256}
	prior := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: a, ConfigSHA256: digest(cfg), OriginalHeadSHA256: headSHA, OriginalJournalSHA256: journalSHA, OriginalState: h.s.Status(), Retained: h.s.Status().Process}, Phase: "adopted", AdoptedUpdater: Identity{PID: 789, StartTicks: 1000, SHA256: a.UpdaterTarget.CandidateSHA256}}
	prior, err = publishUpdaterOnlyAttempt(cfg, p, prior)
	if err != nil {
		t.Fatal(err)
	}
	priorRaw, err := privateFile(updaterOnlyJournalPath(cfg, a.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	next := prior
	next.Phase = "planned"
	next.AdoptedUpdater = Identity{}
	next.Scope.Authorization.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF3"
	source := prior.AdoptedUpdater
	next.Scope.Authorization.UpdaterSource = &source
	target := *a.UpdaterTarget
	target.CandidateSHA256 = strings.Repeat("f", 64)
	next.Scope.Authorization.UpdaterTarget = &target
	next, err = publishUpdaterOnlyAttempt(cfg, p, next)
	if err != nil {
		t.Fatal(err)
	}
	if next.Scope.PreviousAdopted == nil || next.Scope.PreviousAdopted.RequestID != a.RequestID || next.Scope.PreviousAdopted.JournalSHA256 != bytesDigest(priorRaw) {
		t.Fatal("publisher did not bind its adopted predecessor")
	}
	head := updaterOnlyHead{Format: 1, RequestID: next.Scope.Authorization.RequestID, ScopeSHA256: digest(next.Scope)}
	selected, err := selectUpdaterOnlyStartup(cfg, p, head, prior.AdoptedUpdater.SHA256)
	if err != nil || selected.Scope.Authorization.RequestID != a.RequestID {
		t.Fatal("pending replacement blocked installed predecessor", err)
	}
	if raw, _ := privateFile(updaterOnlyJournalPath(cfg, a.RequestID)); bytesDigest(raw) != bytesDigest(priorRaw) {
		t.Fatal("ordinary predecessor selection rewrote adopted receipt")
	}
	// A further pre-start interruption inherits the same single exact receipt.
	third := next
	third.Scope.Authorization.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF4"
	third, err = publishUpdaterOnlyAttempt(cfg, p, third)
	if err != nil {
		t.Fatal(err)
	}
	if third.Scope.PreviousAdopted == nil || *third.Scope.PreviousAdopted != *next.Scope.PreviousAdopted {
		t.Fatal("pre-start continuation lost adopted predecessor")
	}
	// Same bytes remain a valid independent controller takeover. Before Start,
	// selection still belongs to the adopted predecessor; after Start it belongs
	// to this new attempt and must pass its original initial-adoption window.
	same := third
	same.Scope.Authorization.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF6"
	sameTarget := *same.Scope.Authorization.UpdaterTarget
	sameTarget.CandidateSHA256 = prior.AdoptedUpdater.SHA256
	same.Scope.Authorization.UpdaterTarget = &sameTarget
	same, err = publishUpdaterOnlyAttempt(cfg, p, same)
	if err != nil {
		t.Fatal(err)
	}
	sameHead := updaterOnlyHead{Format: 1, RequestID: same.Scope.Authorization.RequestID, ScopeSHA256: digest(same.Scope)}
	selected, err = selectUpdaterOnlyStartup(cfg, p, sameHead, prior.AdoptedUpdater.SHA256)
	if err != nil || selected.Scope.Authorization.RequestID != a.RequestID {
		t.Fatal("same-bytes pre-start shadowed adopted predecessor", err)
	}
	same.Phase = "starting_updater"
	if err := writeJSON(updaterOnlyJournalPath(cfg, same.Scope.Authorization.RequestID), same); err != nil {
		t.Fatal(err)
	}
	selected, err = selectUpdaterOnlyStartup(cfg, p, sameHead, prior.AdoptedUpdater.SHA256)
	if err != nil || selected.Scope.Authorization.RequestID != same.Scope.Authorization.RequestID {
		t.Fatal("recorded same-bytes Start did not select current adoption window", err)
	}
	for _, kind := range []string{"missing receipt", "wrong receipt", "unknown start"} {
		t.Run(kind, func(t *testing.T) {
			bad := third
			bad.Scope.Authorization.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF5"
			copy := *third.Scope.PreviousAdopted
			bad.Scope.PreviousAdopted = &copy
			switch kind {
			case "missing receipt":
				bad.Scope.PreviousAdopted = nil
			case "wrong receipt":
				bad.Scope.PreviousAdopted.JournalSHA256 = strings.Repeat("a", 64)
			case "unknown start":
				bad.Phase = "starting_updater"
			}
			if err := writeJSON(updaterOnlyJournalPath(cfg, bad.Scope.Authorization.RequestID), bad); err != nil {
				t.Fatal(err)
			}
			head := updaterOnlyHead{Format: 1, RequestID: bad.Scope.Authorization.RequestID, ScopeSHA256: digest(bad.Scope)}
			if _, err := selectUpdaterOnlyStartup(cfg, p, head, prior.AdoptedUpdater.SHA256); err == nil {
				t.Fatal("invalid predecessor fallback accepted")
			}
		})
	}
}

func TestUpdaterOnlyRecoveryInstallerWindowReservesRequest(t *testing.T) {
	h, _, a := updaterOnlyLinkedFixture(t)
	for index, action := range []string{"updater_only_takeover", "force", "apply"} {
		t.Run(action, func(t *testing.T) {
			auth := a
			auth.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF" + strconv.Itoa(index+7)
			auth.ExpiresAtMillis = time.Now().Add(10 * time.Minute).UnixMilli()
			if _, err := beginBoundedInstaller(h.cfg, auth, action); err != nil {
				t.Fatal(err)
			}
			err := rejectUpdaterOnlyRequest(h.cfg, auth.RequestID)
			if action == "updater_only_takeover" && err == nil {
				t.Fatal("partial updater-only attempt lost request identity")
			}
			if action != "updater_only_takeover" && err != nil {
				t.Fatal("ordinary maintenance window was rejected", err)
			}
		})
	}
}

func TestUpdaterOnlyRecoveryRequestCannotBecomeAnotherAction(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	a.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
	path := updaterOnlyJournalPath(h.cfg, a.RequestID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	j := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: a, ConfigSHA256: digest(h.cfg), OriginalState: h.s.Status()}, Phase: "adopted"}
	if err := writeJSON(path, j); err != nil {
		t.Fatal(err)
	}
	other := a
	other.Reason = "a different action cannot reuse the original updater-only identity"
	other.UpdaterTarget = nil
	before := h.s.Status()
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p.Maintenance, other, "extend"); err == nil {
		t.Fatal("accepted extend under original updater-only identity")
	}
	if err := h.s.CancelBoundedMaintenance(t.Context(), p.Maintenance, other); err == nil {
		t.Fatal("accepted cancel under original updater-only identity")
	}
	if digest(before) != digest(h.s.Status()) {
		t.Fatal("rejected identity reuse changed state")
	}
}

func TestUpdaterOnlyRecoveryStartupRejectsMissingOrForeignAuthority(t *testing.T) {
	h, p, a := updaterOnlyLinkedFixture(t)
	if err := adoptUpdaterOnlyHandoff(t.Context(), h.s, p, a.UpdaterTarget.CandidateSHA256); err == nil {
		t.Fatal("accepted missing startup authority")
	}
	j := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: a, ConfigSHA256: digest(h.cfg)}, Phase: "starting_updater"}
	if err := os.MkdirAll(filepath.Dir(updaterOnlyJournalPath(h.cfg, a.RequestID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(updaterOnlyJournalPath(h.cfg, a.RequestID), j); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(updaterOnlyHeadPath(h.cfg), updaterOnlyHead{Format: 1, RequestID: a.RequestID, ScopeSHA256: digest(j.Scope)}); err != nil {
		t.Fatal(err)
	}
	if err := adoptUpdaterOnlyHandoff(t.Context(), h.s, p, strings.Repeat("f", 64)); err == nil {
		t.Fatal("accepted unpinned updater candidate")
	}
	if err := writeJSON(updaterOnlyHeadPath(h.cfg), updaterOnlyHead{Format: 1, RequestID: a.RequestID, ScopeSHA256: strings.Repeat("f", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := adoptUpdaterOnlyHandoff(t.Context(), h.s, p, a.UpdaterTarget.CandidateSHA256); err == nil {
		t.Fatal("accepted foreign startup scope")
	}
}

// This isolated fixture uses the actual production serve/adoption entry for the
// replacement. Its committed ledger is synthetic; every process, unit, lock and
// old handoff journal is real. No service or external identity is adopted.
func TestUpdaterOnlySystemdReplacementRetainsCommittedService(t *testing.T) {
	runUpdaterOnlySystemdFixture(t, false)
}

func TestUpdaterOnlySystemdPreStartFreshCASRecovery(t *testing.T) {
	runUpdaterOnlySystemdFixture(t, true)
}

func runUpdaterOnlySystemdFixture(t *testing.T, preStart bool) {
	if os.Getenv("TEND_TEST_SYSTEMD_USER") != "1" {
		t.Skip("opt-in: isolated user-systemd unit and exact replacement artifact")
	}
	candidate := os.Getenv("TEND_TEST_NEW_UPDATER")
	if !filepath.IsAbs(candidate) {
		t.Fatal("exact replacement updater artifact required")
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
	if err := copyAtomic(self, fixed, 0o755); err != nil {
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
	cfg := Config{InstanceID: "bounded-fixture", Role: "server", StateDir: stateDir, InitialBinary: self, Args: []string{"-test.run=^TestBoundedSystemdProcessHelper$"}, EnvFiles: []string{envFile}, DataFiles: []string{dataFile}, LogPath: filepath.Join(root, "child.log"), AuthoritySocket: filepath.Join(root, "absent.sock"), CoordinatorSocket: filepath.Join(root, "absent.sock"), AuthorizeURL: "http://localhost/updater/authorize", HealthURL: "http://localhost/updater/health", Protocol: protocol.CurrentRunnerProtocol, StorageVersion: 1, StopTimeoutSeconds: 2, HealthTimeoutSeconds: 3}
	normalizeConfig(&cfg)
	configFile := filepath.Join(root, "updater.json")
	if err := writeJSON(configFile, cfg); err != nil {
		t.Fatal(err)
	}
	selfSHA, err := hashFile(self)
	if err != nil {
		t.Fatal(err)
	}
	candidateSHA, err := hashFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if selfSHA == candidateSHA {
		t.Fatal("fixture must replace original pinned updater with distinct bytes")
	}
	info, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	version := Version{Protocol: cfg.Protocol, StorageVersion: 1, ReleaseID: "updater-only-fixture", ManifestSHA256: strings.Repeat("a", 64), Binary: self, Artifact: release.Artifact{Role: "server", OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "server", SHA256: selfSHA, Size: info.Size()}}
	statePath := filepath.Join(stateDir, "state.json")
	if err := writeJSON(statePath, State{Format: 1, InstanceID: cfg.InstanceID, Current: version}); err != nil {
		t.Fatal(err)
	}
	unit := "tend-updater-only-fixture-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".service"
	var child, predecessor Identity
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = (systemdUser{}).call(cleanup, "stop", unit)
		for _, id := range []Identity{child, predecessor} {
			if id.PID > 1 {
				if err := (LinuxProcess{}).Stop(cleanup, id, 3*time.Second); err != nil {
					t.Error(err)
				}
			}
		}
		_, _ = (systemdUser{}).call(cleanup, "reset-failed", unit)
	})
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--user", "--unit="+unit, "--property=Type=simple", "--property=KillMode=process", "--property=Restart=on-failure", "--property=TimeoutStopSec=4s", "--setenv=TEND_BOUNDED_UPDATER_HELPER=1", "--", fixed, "serve", "--config", configFile)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start fixture: %v %s", err, raw)
	}
	var st State
	boundedSystemdWait(t, ctx, "initial real committed child", func() bool {
		var err error
		st, err = Call(ctx, cfg, "status", ControlRequest{})
		if err == nil && st.Process.PID > 1 && st.Operation != nil && st.Operation.Phase == "committed" {
			child = st.Process
			return true
		}
		return false
	})
	if _, err := (systemdUser{}).call(ctx, "stop", unit); err != nil {
		t.Fatal(err)
	}
	predecessor = boundedRealHelper(t, ctx, root, "predecessor")
	if err := (LinuxProcess{}).Stop(ctx, predecessor, 3*time.Second); err != nil {
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
	configRaw, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	previous := Binding{UpgradeID: "fixture-previous", InstanceID: cfg.InstanceID, ProcessInstanceID: predecessor.LaunchID, ReleaseID: "fixture-previous", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: predecessor.SHA256}
	target := Binding{UpgradeID: "updater-only-fixture", InstanceID: cfg.InstanceID, ProcessInstanceID: child.LaunchID, ReleaseID: version.ReleaseID, ManifestSHA256: version.ManifestSHA256, ArtifactSHA256: child.SHA256}
	p := CommittedHandoffPlan{Maintenance: protocol.CommittedMaintenancePlan{MaintenanceID: target.UpgradeID, RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEV", LinkRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEW", Previous: previous, PreviousProcess: predecessor, Successor: target, ConfigSHA256: digest(cfg)}, Unit: unit, UnitSHA256: unitSHA, ManagerSHA256: policy, ManagerControlGroup: manager["ControlGroup"], UpdaterBinary: fixed, ConfigFile: configFile, ConfigFileSHA256: bytesDigest(configRaw), Candidate: self, CandidateSHA256: selfSHA, LockDevice: dev, LockInode: ino}
	receipt := &protocol.CommittedMaintenanceReceipt{MaintenanceID: p.Maintenance.MaintenanceID, RequestID: p.Maintenance.RequestID, Previous: previous, Successor: target, Process: child, Generation: 10, Epoch: "fixture-service-epoch", FirstSequence: 1, SecondSequence: 2}
	st.Format, st.Generation, st.UpdaterEpoch = 4, 10, "fixture-old-updater"
	st.Operation = nil
	st.CommittedProof = &CommittedProof{Binding: target, Version: version, Process: child}
	st.Maintenance = &CommittedMaintenance{Plan: p.Maintenance, Phase: "ledger_linked", Target: version, Process: child, Irreversible: true, Epoch: receipt.Epoch, FirstSequence: 1, LastSequence: 2, HealthCount: 2, Receipt: receipt}
	st.Bounded = &protocol.BoundedMaintenanceState{Phase: "complete", Outcome: "installed_recovery_pending", SourceExit: &protocol.BoundedProcessExit{Process: predecessor, ConfirmedAtMillis: time.Now().UnixMilli(), Method: "fixture exact stop"}}
	if err := writeJSON(statePath, st); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(committedJournalPath(cfg, p.Maintenance.MaintenanceID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedJournalPath(cfg, p.Maintenance.MaintenanceID), CommittedHandoffJournal{Plan: p, Phase: "starting_adopt", Generation: 4}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(committedHeadPath(cfg), committedHandoffHead{MaintenanceID: p.Maintenance.MaintenanceID, PlanSHA256: digest(p)}); err != nil {
		t.Fatal(err)
	}
	originalHead, originalJournal, err := updaterOnlyOriginalFiles(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (systemdUser{}).call(ctx, "start", unit); err != nil {
		t.Fatal(err)
	}
	boundedSystemdWait(t, ctx, "old helper reopened linked state", func() bool {
		var e error
		st, e = Call(ctx, cfg, "status", ControlRequest{})
		return e == nil && st.Generation == 11
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
	a := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: st.Generation, ExpectedPhase: "ledger_linked", PlanSHA256: digest(p.Maintenance), Role: "server", Source: predecessor, Target: target, Reason: "replace only a committed service updater", ExpiresAtMillis: time.Now().Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(), UpdaterSource: &oldUpdater, UpdaterLock: &protocol.BoundedDataIdentity{Device: dev, Inode: ino}, UpdaterTarget: &protocol.BoundedUpdaterTarget{Unit: unit, UnitSHA256: unitSHA, ManagerSHA256: policy, UpdaterBinary: fixed, Candidate: candidate, CandidateSHA256: candidateSHA, ConfigFile: configFile, ConfigFileSHA256: bytesDigest(configRaw)}}
	a.DataIdentity, err = boundedServerDataIdentity(cfg)
	if err != nil {
		t.Fatal(err)
	}
	planFile, authFile := filepath.Join(root, "plan.json"), filepath.Join(root, "auth.json")
	if err := writeJSON(planFile, p); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(authFile, a); err != nil {
		t.Fatal(err)
	}
	// Stale CAS, changed retained proof and lock fail before any updater effect.
	for index, kind := range []string{"stale", "lock", "retained", "physical retained"} {
		t.Run(kind, func(t *testing.T) {
			bad := a
			bad.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF" + strconv.Itoa(index+3)
			lockCopy := *a.UpdaterLock
			bad.UpdaterLock = &lockCopy
			switch kind {
			case "stale":
				bad.ExpectedGeneration++
			case "lock":
				bad.UpdaterLock.Inode++
			case "retained", "physical retained":
				changed := cloneState(st)
				changed.CommittedProof.Process.StartTicks++
				if kind == "physical retained" {
					changed.Process.StartTicks++
					changed.Maintenance.Process.StartTicks++
					changed.Maintenance.Receipt.Process.StartTicks++
				}
				if err := writeJSON(statePath, changed); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := writeJSON(statePath, st); err != nil {
						t.Error(err)
					}
				}()
			}
			if _, err := takeoverCommittedUpdaterOnly(ctx, cfg, p, bad); err == nil {
				t.Fatal("accepted invalid source")
			}
			if actual, err := (LinuxProcess{}).Inspect(ctx, oldUpdater.PID); err != nil || actual != oldUpdater {
				t.Fatal("invalid authority stopped updater", err)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(stateDir, "fixture-hang-mutex"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	boundedSystemdWait(t, ctx, "old updater ignores SIGTERM and holds mutex", func() bool { _, err := os.Stat(filepath.Join(stateDir, "fixture-mutex-held")); return err == nil })
	if preStart {
		prior := a
		prior.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
		if _, err := beginBoundedInstaller(cfg, prior, "updater_only_takeover"); err != nil {
			t.Fatal(err)
		}
		old := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: prior, ConfigSHA256: digest(cfg), OriginalHeadSHA256: originalHead, OriginalJournalSHA256: originalJournal, OriginalState: st, Retained: child}, Phase: "installing_updater"}
		if err := os.MkdirAll(filepath.Dir(updaterOnlyJournalPath(cfg, prior.RequestID)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(updaterOnlyJournalPath(cfg, prior.RequestID), old); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(updaterOnlyHeadPath(cfg), updaterOnlyHead{Format: 1, RequestID: prior.RequestID, ScopeSHA256: digest(old.Scope)}); err != nil {
			t.Fatal(err)
		}
		if _, err := (systemdUser{}).call(ctx, "--no-block", "stop", unit); err != nil {
			t.Fatal(err)
		}
		if err := (LinuxProcess{}).Crash(ctx, oldUpdater, time.Second); err != nil {
			t.Fatal(err)
		}
		boundedSystemdWait(t, ctx, "definite pre-start source exit and idle manager", func() bool { m, _, err := managerSnapshot(ctx, unit); return err == nil && updaterOnlyManagerIdle(m) })
	}
	if _, err := BoundedCommand(ctx, cfg, "takeover", planFile, authFile, a.RequestID, a.ExpectedGeneration, a.ExpectedPhase); err != nil {
		raw, _ := os.ReadFile(filepath.Join(root, "child.log"))
		t.Fatalf("updater-only takeover: %v; child log: %s", err, raw)
	}
	var adopted updaterOnlyJournal
	boundedSystemdWait(t, ctx, "production serve adopted independent updater-only authority", func() bool {
		var err error
		adopted, err = readUpdaterOnlyJournal(cfg, a.RequestID)
		return err == nil && adopted.Phase == "adopted"
	})
	if adopted.AdoptedUpdater.SHA256 != candidateSHA || adopted.AdoptedUpdater.PID == oldUpdater.PID {
		t.Fatal("wrong replacement updater")
	}
	if actual, err := (LinuxProcess{}).Inspect(ctx, child.PID); err != nil || actual != child {
		t.Fatal("updater-only replacement stopped or restarted service", err)
	}
	if h, j, err := updaterOnlyOriginalFiles(cfg, p); err != nil || h != originalHead || j != originalJournal {
		t.Fatal("original committed history mutated", err)
	}
	latest, err := readState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	normalized := cloneState(latest)
	normalized.Generation, normalized.UpdaterEpoch, normalized.UpdaterBootID = st.Generation, st.UpdaterEpoch, st.UpdaterBootID
	if digest(normalized) != digest(st) {
		t.Fatal("replacement changed service, original maintenance or bounded deadline state")
	}
	if _, err := BoundedCommand(ctx, cfg, "takeover", planFile, authFile, a.RequestID, a.ExpectedGeneration, a.ExpectedPhase); err != nil {
		t.Fatal("original replay should observe prior attempt", err)
	}
	if actual, err := (LinuxProcess{}).Inspect(ctx, adopted.AdoptedUpdater.PID); err != nil || actual != adopted.AdoptedUpdater {
		t.Fatal("original replay restarted updater", err)
	}
	// Publish a second real installer attempt, then interrupt it before Stop.
	// The already installed updater must retain its exact adopted startup proof.
	pendingAuth := a
	pendingAuth.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF7"
	pendingAuth.ExpectedGeneration = latest.Generation
	pendingAuth.UpdaterSource = &adopted.AdoptedUpdater
	pendingTarget := *a.UpdaterTarget
	pendingAuth.UpdaterTarget = &pendingTarget
	if _, err := beginBoundedInstaller(cfg, pendingAuth, "updater_only_takeover"); err != nil {
		t.Fatal(err)
	}
	pending := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: pendingAuth, ConfigSHA256: digest(cfg), OriginalHeadSHA256: originalHead, OriginalJournalSHA256: originalJournal, OriginalState: latest, Retained: child}, Phase: "planned"}
	pending, err = publishUpdaterOnlyAttempt(cfg, p, pending)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Scope.PreviousAdopted == nil || pending.Scope.PreviousAdopted.RequestID != a.RequestID {
		t.Fatal("pending production publisher did not preserve installed authority")
	}
	// An ordinary later reopen remains valid after legitimate completion state
	// changes. Its durable adopted receipt does not freeze the entire old state.
	if _, err := (systemdUser{}).call(ctx, "stop", unit); err != nil {
		t.Fatal(err)
	}
	latest.Bounded.Outcome = "completed"
	latest.Maintenance.LastError = ""
	if err := writeJSON(statePath, latest); err != nil {
		t.Fatal(err)
	}
	if _, err := (systemdUser{}).call(ctx, "start", unit); err != nil {
		t.Fatal(err)
	}
	boundedSystemdWait(t, ctx, "ordinary patched updater reopen after completion", func() bool {
		m, _, e := managerSnapshot(ctx, unit)
		if e != nil || m["MainPID"] == "0" || m["MainPID"] == strconv.Itoa(adopted.AdoptedUpdater.PID) {
			return false
		}
		now, e := Call(ctx, cfg, "status", ControlRequest{})
		return e == nil && now.Process == child && now.Bounded.Outcome == "completed"
	})
	if err := verifyUpdaterOnlyRetained(ctx, child); err != nil {
		t.Fatal(err)
	}
	currentManager, _, err := managerSnapshot(ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	currentPID, err := strconv.Atoi(currentManager["MainPID"])
	if err != nil {
		t.Fatal(err)
	}
	currentUpdater, err := (LinuxProcess{}).Inspect(ctx, currentPID)
	if err != nil {
		t.Fatal(err)
	}
	currentState, err := readState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	sameAuth := a
	sameAuth.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF8"
	sameAuth.ExpectedGeneration = currentState.Generation
	sameAuth.UpdaterSource = &currentUpdater
	if err := writeJSON(authFile, sameAuth); err != nil {
		t.Fatal(err)
	}
	if _, err := BoundedCommand(ctx, cfg, "takeover", planFile, authFile, sameAuth.RequestID, sameAuth.ExpectedGeneration, sameAuth.ExpectedPhase); err != nil {
		t.Fatal("exact same-bytes controller takeover failed", err)
	}
	var sameAdopted updaterOnlyJournal
	boundedSystemdWait(t, ctx, "same-bytes controller adopted under its own original window", func() bool {
		var err error
		sameAdopted, err = readUpdaterOnlyJournal(cfg, sameAuth.RequestID)
		return err == nil && sameAdopted.Phase == "adopted"
	})
	if sameAdopted.AdoptedUpdater.PID == currentUpdater.PID || sameAdopted.AdoptedUpdater.SHA256 != currentUpdater.SHA256 {
		t.Fatal("same-bytes takeover did not replace exactly its controller")
	}
	if err := verifyUpdaterOnlyRetained(ctx, child); err != nil {
		t.Fatal("same-bytes takeover affected committed service", err)
	}
	if _, err := (LinuxProcess{}).Inspect(ctx, oldUpdater.PID); !errors.Is(err, ErrNotRunning) {
		t.Fatal("original controller still live")
	}
	evidenceName := "updater-only-postcommit"
	if preStart {
		evidenceName = "updater-only-pre-start-recovery"
	}
	boundedFixtureEvidence(t, evidenceName, map[string]any{"unit": unit, "old_updater": oldUpdater, "new_updater": adopted.AdoptedUpdater, "retained_committed_service": child, "exited_original_predecessor": predecessor, "original_head_sha256": originalHead, "original_journal_sha256": originalJournal, "original_plan": p, "original_authorization": a, "adopted_continuation": adopted, "unchanged_service_and_deadlines": true, "ordinary_completion_reopen": true, "pending_replacement_preserved_adopted_startup": true, "pending_attempt": pending, "same_bytes_source_controller": currentUpdater, "same_bytes_adopted": sameAdopted})
}
