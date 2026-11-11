package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type Options struct {
	ConfigFile            string
	ValidateConfiguration func(context.Context, string, string, string) error
	Process               Process
	Authority             Authority
	Now                   func() time.Time
	NewID                 func() (string, error)
	BootClock             func() (string, int64, error)
}
type Supervisor struct {
	selfJournal        *SelfUpdateJournal
	selfUpdating       bool
	selfIdentity       Identity
	observedProcess    ProcessObservation
	verifiedProcess    Identity
	externalGeneration *uint64
	durable            State
	persistenceErr     error
	writeState         func(string, State) error
	mu                 sync.Mutex
	cfg                Config
	opts               Options
	state              State
	lock               *os.File
}

func Open(cfg Config, opts Options) (*Supervisor, error) { return openSupervisor(cfg, opts, nil) }

func openSupervisor(cfg Config, opts Options, heldLock *os.File) (*Supervisor, error) {
	if err := supported(); err != nil {
		return nil, err
	}
	if cfg.InstanceID == "" || len(cfg.InstanceID) > 256 || cfg.Role != "server" && cfg.Role != "runner" && cfg.Role != "maintainer" || !filepath.IsAbs(cfg.StateDir) || !filepath.IsAbs(cfg.InitialBinary) || !filepath.IsAbs(cfg.LogPath) {
		return nil, errors.New("updater: invalid service configuration")
	}
	if cfg.ServiceConfig != "" {
		if cfg.Role != "runner" || !filepath.IsAbs(cfg.ServiceConfig) || filepath.Clean(cfg.ServiceConfig) != cfg.ServiceConfig {
			return nil, errors.New("updater: invalid fixed service configuration path")
		}
		found := false
		for i, arg := range cfg.Args {
			if (arg == "-config" || arg == "--config") && i+1 < len(cfg.Args) && cfg.Args[i+1] == cfg.ServiceConfig {
				found = true
			}
		}
		if !found {
			return nil, errors.New("updater: service configuration must match managed arguments")
		}
		for _, p := range append(append([]string{}, cfg.EnvFiles...), cfg.DataFiles...) {
			if p == cfg.ServiceConfig {
				return nil, errors.New("updater: configuration overlaps data or credentials")
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range cfg.DataFiles {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || seen[p] {
			return nil, errors.New("updater: invalid or duplicate data file")
		}
		seen[p] = true
		for _, env := range cfg.EnvFiles {
			if p == env {
				return nil, errors.New("updater: credentials cannot be rollback data")
			}
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("updater: state directory must be private and not a symlink")
	}
	lock := heldLock
	if lock == nil {
		lock, err = lockDir(cfg.StateDir)
		if err != nil {
			return nil, err
		}
	}
	fail := func(err error) (*Supervisor, error) { _ = unlock(lock); return nil, err }
	if opts.Process == nil {
		opts.Process = LinuxProcess{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = newID
	}
	if opts.BootClock == nil {
		opts.BootClock = BootClock
	}
	if cfg.StopTimeoutSeconds <= 0 {
		cfg.StopTimeoutSeconds = 30
	}
	if cfg.HealthTimeoutSeconds <= 0 {
		cfg.HealthTimeoutSeconds = 120
	}
	s := &Supervisor{cfg: cfg, opts: opts, lock: lock, writeState: func(path string, state State) error { return writeJSON(path, state) }}
	state, err := readState(s.statePath())
	if os.IsNotExist(err) {
		state = State{Format: 1, InstanceID: cfg.InstanceID}
	} else if err != nil {
		return fail(err)
	}
	if (state.Format < 1 || state.Format > 5) || state.InstanceID != cfg.InstanceID {
		return fail(errors.New("updater: durable state belongs to a different instance"))
	}
	if state.Format == 2 && (state.Generation == 0 || state.UpdaterEpoch == "" || state.Recovery == nil) {
		return fail(errors.New("updater: incomplete format 2 recovery ownership"))
	}
	if state.Format == 3 && (state.Generation == 0 || state.UpdaterEpoch == "" || state.Maintenance == nil) {
		return fail(errors.New("updater: incomplete committed maintenance ownership"))
	}
	if state.Format == 4 && (state.Generation == 0 || state.UpdaterEpoch == "" || state.Bounded == nil && len(state.BoundedHistory) == 0 || state.Maintenance == nil && state.Operation == nil) {
		return fail(errors.New("updater: incomplete bounded maintenance ownership"))
	}
	if state.Format == 5 && (state.Generation == 0 || state.UpdaterEpoch == "") {
		return fail(errors.New("updater: incomplete critical operation ownership"))
	}
	if state.Format >= 2 {
		epoch, err := opts.NewID()
		if err != nil {
			return fail(err)
		}
		boot, _, err := opts.BootClock()
		if err != nil {
			return fail(err)
		}
		state.UpdaterEpoch = epoch
		state.UpdaterBootID = boot
		state.Generation++
	}
	if err := s.writeState(s.statePath(), state); err != nil {
		return fail(err)
	}
	s.state = state
	s.durable = cloneState(state)
	return s, nil
}
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := unlock(s.lock)
	s.lock = nil
	return err
}
func (s *Supervisor) statePath() string { return filepath.Join(s.cfg.StateDir, "state.json") }
func (s *Supervisor) durabilityError() error {
	// Every service mutation checks this under mu. An HTTP preflight alone
	// cannot serialize admission while a concurrent request is decoding.
	if s.selfUpdating {
		return ErrUnknown
	}
	if s.persistenceErr != nil {
		return fmt.Errorf("updater: durability unknown; reopen supervisor before further action: %w", s.persistenceErr)
	}
	if s.lock == nil {
		return errors.New("updater: supervisor is closed")
	}
	held, heldErr := s.lock.Stat()
	current, currentErr := os.Lstat(filepath.Join(s.cfg.StateDir, "supervisor.lock"))
	if heldErr != nil || currentErr != nil || !os.SameFile(held, current) {
		return errors.New("updater: supervisor lock inode changed; no further writes allowed")
	}
	return nil
}
func (s *Supervisor) save() error {
	if err := s.durabilityError(); err != nil {
		return err
	}
	if receipt := operationRollbackReceipt(s.state); receipt != nil {
		s.state.RollbackProof = &RollbackProof{Binding: receipt.Binding, Version: s.state.Current, Process: s.state.Process}
		s.state.CommittedProof = nil
	}
	if op := s.state.Operation; op != nil && op.Kind == "upgrade" && op.Phase == "committed" && op.Irreversible && s.state.Current == op.Candidate {
		s.state.CommittedProof = &CommittedProof{Binding: op.Binding, Version: s.state.Current, Process: s.state.Process}
		s.state.RollbackProof = nil
	}
	if op := s.state.Operation; op != nil && op.Kind == "upgrade" && done(op) {
		if s.state.Terminals == nil {
			s.state.Terminals = make(map[string]TerminalReceipt)
		}
		s.state.Terminals[op.ID] = TerminalReceipt{Binding: op.Binding, Phase: op.Phase}
	}
	if s.state.Format >= 2 {
		s.state.Generation++
	}
	if err := s.writeState(s.statePath(), cloneState(s.state)); err != nil {
		s.persistenceErr = err
		return s.durabilityError()
	}
	s.durable = cloneState(s.state)
	return nil
}
func cloneState(v State) State {
	raw, _ := json.Marshal(v)
	var cloned State
	_ = json.Unmarshal(raw, &cloned)
	return cloned
}
func (s *Supervisor) Status() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := cloneState(s.durable)
	if s.persistenceErr != nil {
		result.LastError = "updater: durability unknown; reopen supervisor before further action"
	}
	return result
}

// Bootstrap adopts a previously launched idle process. The caller must first
// complete the coordinator's idle bootstrap preflight. It does not stop it.
func (s *Supervisor) Bootstrap(ctx context.Context, pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if s.state.Current.Binary != "" {
		return errors.New("updater: service is already bootstrapped")
	}
	expected, err := hashFile(s.cfg.InitialBinary)
	if err != nil {
		return err
	}
	id, err := s.opts.Process.Inspect(ctx, pid)
	if err != nil {
		return err
	}
	if id.SHA256 != expected {
		return errors.New("updater: bootstrap executable does not match configured service")
	}
	if id.LaunchID == "" {
		return errors.New("updater: bootstrap requires a launch ID matching the service runtime instance")
	}

	if v, ok := s.opts.Process.(interface {
		VerifyCommand(context.Context, int, Config) error
	}); ok {
		if err := v.VerifyCommand(ctx, pid, s.cfg); err != nil {
			return err
		}
	}
	info, err := os.Stat(s.cfg.InitialBinary)
	if err != nil {
		return err
	}
	v := Version{Protocol: s.cfg.Protocol, StorageVersion: s.cfg.StorageVersion, ReleaseID: "bootstrap-" + expected[:16], Artifact: release.Artifact{Role: s.cfg.Role, Size: info.Size(), SHA256: expected}, Binary: s.cfg.InitialBinary}
	// Copy the actual initial executable into a retained version directory so a
	// later external replacement of the original path cannot erase rollback.
	dir := filepath.Join(s.cfg.StateDir, "versions", v.ReleaseID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	v.Binary = filepath.Join(dir, "tend-"+s.cfg.Role)
	if err := copyAtomic(s.cfg.InitialBinary, v.Binary, 0o755); err != nil {
		return err
	}
	if sum, err := hashFile(v.Binary); err != nil || sum != expected {
		return errors.New("updater: bootstrap copy verification failed")
	}
	s.state.Current = v
	s.state.Process = id
	return s.save()
}
func done(op *Operation) bool {
	return op == nil || op.Phase == "committed" || op.Phase == "rolled_back" || op.Phase == "cancelled"
}

// Upgrade validates and installs the bundle without disturbing the running
// process. The operation ID is stable across retries and payload conflicts fail.
func (s *Supervisor) Upgrade(ctx context.Context, id, dir string) error {
	return s.UpgradeConfigured(ctx, id, dir, nil)
}

func (s *Supervisor) UpgradeConfigured(ctx context.Context, id, dir string, configuration *protocol.RunnerConfiguration) error {
	if !release.ValidID(id) {
		return errors.New("updater: invalid upgrade identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if !maintenanceTerminal(s.state.Maintenance) {
		return errors.New("updater: committed maintenance owns this service")
	}
	if s.state.Recovery != nil && s.state.Recovery.Phase != "ledger_linked" {
		return errors.New("updater: forward recovery owns this window")
	}
	for _, r := range s.state.Recoveries {
		if r.Plan.Original.UpgradeID == id || r.Plan.RecoveryID == id {
			return errors.New("updater: historical forward recovery identity cannot start a new upgrade")
		}
	}
	if s.state.Current.Binary == "" {
		return errors.New("updater: bootstrap required")
	}
	var staged *StagedConfiguration
	if saved, ok := s.state.Configurations[id]; ok {
		if configuration == nil || !sameConfigurationTarget(saved.Target, *configuration) {
			return errors.New("updater: configuration identity conflicts with staged request")
		}
		staged = &saved
	} else if configuration != nil {
		return errors.New("updater: configuration must be staged locally first")
	}
	if receipt, ok := s.state.Terminals[id]; ok {
		if staged != nil && receipt.Binding.ManifestSHA256 != staged.ManifestSHA256 {
			return errors.New("updater: configuration manifest conflict")
		}
		if receipt.Phase == "cancelled" {
			return errors.New("updater: operation was cancelled")
		}
		bundle, err := release.LoadService(dir, s.cfg.Role)
		if err != nil {
			return err
		}
		if receipt.Binding.ManifestSHA256 != bundle.ManifestSHA256 {
			return errors.New("updater: upgrade identity conflicts with historical payload")
		}
		return nil
	}
	if s.selfUpdating {
		return ErrUnknown
	}
	candidate, err := s.stage(dir)
	if err != nil {
		return err
	}
	if err := s.requireSelfUpdate(ctx, id, dir); err != nil {
		return err
	}
	if staged != nil && (candidate.ManifestSHA256 != staged.ManifestSHA256 || candidate.Artifact.SHA256 != s.state.Current.Artifact.SHA256) {
		return errors.New("updater: configuration maintenance requires its pinned manifest and current binary")
	}
	if op := s.state.Operation; op != nil && op.ID == id {
		if op.Candidate.ManifestSHA256 != candidate.ManifestSHA256 || op.Candidate.Artifact.SHA256 != candidate.Artifact.SHA256 {
			return errors.New("updater: upgrade identity conflicts with previous payload")
		}
		return nil
	}
	if !done(s.state.Operation) {
		return errors.New("updater: another operation is unfinished")
	}
	if s.opts.Authority == nil {
		return errors.New("updater: fixed authority is required")
	}
	processID, err := s.opts.NewID()
	if err != nil {
		return err
	}
	b := Binding{UpgradeID: id, InstanceID: s.cfg.InstanceID, ProcessInstanceID: processID, ReleaseID: candidate.ReleaseID, ManifestSHA256: candidate.ManifestSHA256, ArtifactSHA256: candidate.Artifact.SHA256}
	if bounded := s.state.Bounded; bounded != nil {
		if s.state.BoundedHistory == nil {
			s.state.BoundedHistory = map[string]protocol.BoundedMaintenanceState{}
		}
		s.state.BoundedHistory[bounded.Authorization.RequestID] = *bounded
		s.state.Bounded = nil
		s.state.BoundedRunnerGrant = nil
	}
	s.state.Operation = &Operation{Configuration: staged, ID: id, Kind: "upgrade", Binding: b, Previous: s.state.Current, Candidate: candidate, PreviousProcess: s.state.Process, LaunchID: processID, Phase: "staged", StartedAt: s.opts.Now()}
	return s.save()
}

// Restart rejects an operator shortcut until a coordinator can prove idle.
// Unexpected process exit recovery does not signal a running service.
func (s *Supervisor) Restart(string) error {
	return errors.New("updater: managed restart requires coordinated idle maintenance; direct restart is disabled")
}
func (s *Supervisor) Cancel(id string) error { return s.CancelExpected(id, "") }

// CancelExpected compares the durable phase under the same lock as cancellation.
func (s *Supervisor) CancelExpected(id, expectedPhase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !maintenanceTerminal(s.state.Maintenance) {
		return errors.New("updater: committed maintenance owns this service")
	}
	if s.state.Recovery != nil && s.state.Recovery.Phase != "ledger_linked" {
		return errors.New("updater: forward recovery owns this window")
	}
	if err := s.durabilityError(); err != nil {
		return err
	}
	if !release.ValidID(id) {
		return errors.New("updater: invalid operation identity")
	}
	op := s.state.Operation
	if expectedPhase == "staged" && (op == nil || op.ID != id) {
		if receipt, ok := s.state.Terminals[id]; ok {
			if receipt.Phase != "cancelled" {
				return errors.New("updater: historical operation already completed")
			}
			// A repeated cancellation must retain the original complete candidate.
			return s.save()
		}
		if s.state.Terminals == nil {
			s.state.Terminals = make(map[string]TerminalReceipt)
		}
		s.state.Terminals[id] = TerminalReceipt{Binding: Binding{UpgradeID: id, InstanceID: s.cfg.InstanceID}, Phase: "cancelled"}
		return s.save()
	}
	if op == nil || op.ID != id {
		return errors.New("updater: unknown operation")
	}
	if expectedPhase == "staged" && op.Phase == "cancelled" {
		return s.save()
	}
	if expectedPhase != "" && op.Phase != expectedPhase {
		return errors.New("updater: cancellation phase changed")
	}
	if done(op) {
		return nil
	}
	op.CancelRequested = true
	if op.Phase == "staged" {
		op.Phase = "cancelled"
	}
	// Once stopping has begun, cancellation does not guess that reverting data is
	// safe. Complete/reconcile this host; the coordinator cancels later hosts.
	return s.save()
}
func (s *Supervisor) Retry(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !maintenanceTerminal(s.state.Maintenance) {
		return errors.New("updater: committed maintenance owns this service")
	}
	if s.state.Recovery != nil && s.state.Recovery.Phase != "ledger_linked" {
		return errors.New("updater: forward recovery owns this window")
	}
	if err := s.durabilityError(); err != nil {
		return err
	}
	if s.state.Operation == nil || s.state.Operation.ID != id {
		return errors.New("updater: unknown operation")
	}
	s.state.Operation.LastError = ""
	return s.save()
}
func (s *Supervisor) grantValid(op *Operation) error {
	if op.Kind != "upgrade" {
		return nil
	}
	if op.Grant == nil || !op.Grant.Allowed || op.Grant.Binding != op.Binding {
		return errors.New("updater: no matching installation grant")
	}
	boot, now, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	if boot != op.GrantBootID || now >= op.GrantDeadlineMillis {
		return errors.New("updater: installation grant expired; coordinator reconciliation required")
	}
	return nil
}
func (s *Supervisor) authorize(ctx context.Context, op *Operation) error {
	boot, start, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	g, err := s.opts.Authority.Authorize(ctx, op.Binding)
	if err != nil {
		return err
	}
	if !g.Allowed || g.Binding != op.Binding || g.RemainingMillis <= 0 || g.RemainingMillis > 30*60*1000 {
		return errors.New("updater: fixed authority denied or returned invalid grant")
	}
	op.Grant = &g
	op.GrantBootID = boot
	op.GrantDeadlineMillis = start + g.RemainingMillis
	return s.grantValid(op)
}
func (s *Supervisor) Step(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selfUpdating {
		j, err := readSelf(s.cfg)
		if err != nil {
			return err
		}
		rejected, err := rejectedSelfBeforeStop(s.cfg, j)
		if err != nil {
			return err
		}
		if rejected == nil || j.Plan.Updater != s.selfIdentity {
			return nil
		}
		s.selfUpdating = false
	}
	if err := s.durabilityError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handled, err := s.boundedRunnerStep(ctx); handled {
		if err != nil && s.persistenceErr == nil && s.state.Operation != nil && s.state.Operation.LastError != err.Error() {
			s.state.Operation.LastError = err.Error()
			if saveErr := s.save(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if m := s.state.Maintenance; !maintenanceTerminal(m) {
		handled, err := s.boundedMaintenanceStep(ctx)
		if !handled && err == nil {
			if b := s.state.Bounded; b != nil {
				remaining, e := s.boundedRemaining()
				if e != nil {
					err = e
				} else {
					stepCtx, cancel := context.WithTimeout(ctx, remaining)
					err = s.maintenanceStep(stepCtx)
					cancel()
				}
			} else {
				err = s.maintenanceStep(ctx)
			}
		}
		if err != nil && s.persistenceErr == nil && (m.LastError != err.Error() || digest(s.state) != digest(s.durable)) {
			m.LastError = err.Error()
			if saveErr := s.save(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if r := s.state.Recovery; r != nil && r.Phase != "ledger_linked" {
		err := s.recoveryStep(ctx)
		if err != nil && s.persistenceErr == nil {
			r.LastError = err.Error()
			if e := s.save(); e != nil {
				return e
			}
		}
		return err
	}
	op := s.state.Operation
	if done(op) {
		err := s.monitor(ctx)
		message := ""
		if err != nil {
			message = err.Error()
		}
		if s.state.LastError != message {
			s.state.LastError = message
			if saveErr := s.save(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	err := s.step(ctx, op)
	if err != nil && (op.LastError != err.Error() || digest(s.state) != digest(s.durable)) {
		op.LastError = err.Error()
		if saveErr := s.save(); saveErr != nil {
			return fmt.Errorf("updater: persist failed operation: %w", saveErr)
		}
	}
	return err
}
func (s *Supervisor) step(ctx context.Context, op *Operation) error {
	switch op.Phase {
	case "staged":
		if op.Configuration != nil {
			if err := s.verifyStagedConfiguration(op.Configuration); err != nil {
				return err
			}
		}
		if err := s.authorize(ctx, op); err != nil {
			return err
		}
		// Equal bytes skip process replacement; authorization is still checked and
		// the manifest identity is preserved as the selected release.
		if op.Configuration == nil && op.Candidate.Artifact.SHA256 == s.state.Current.Artifact.SHA256 {
			s.state.Current = op.Candidate
			op.Irreversible = true
			op.Phase = "committed"
			return s.save()
		}
		op.Phase = "stopping"
		return s.save()
	case "stopping":
		if err := s.grantValid(op); err != nil {
			return s.recoverStopping(ctx, op)
		}
		if op.Configuration != nil {
			if err := s.verifyStagedConfiguration(op.Configuration); err != nil {
				return err
			}
		}
		if err := s.opts.Process.Stop(ctx, op.PreviousProcess, time.Duration(s.cfg.StopTimeoutSeconds)*time.Second); err != nil {
			return err
		}
		op.Phase = "stopped"
		return s.save()
	case "stopped":
		if err := s.grantValid(op); err != nil {
			return s.recoverUnlaunched(ctx, op)
		}
		if op.Kind == "upgrade" {
			op.BackupDir = filepath.Join(s.cfg.StateDir, "backups", op.ID)
			if err := backupDataContext(ctx, s.cfg, op); err != nil {
				return err
			}
		}
		op.Phase = "backed_up"
		return s.save()
	case "backed_up":
		if err := s.grantValid(op); err != nil {
			return s.recoverUnlaunched(ctx, op)
		}
		if op.Configuration != nil {
			op.Phase = "config_applying"
			return s.save()
		}
		op.Phase = "starting"
		if err := s.save(); err != nil {
			return err
		}
		return s.start(ctx, op, false)
	case "config_applying":
		if err := s.grantValid(op); err != nil {
			return s.recoverUnlaunched(ctx, op)
		}
		if err := s.applyConfiguration(op.Configuration); err != nil {
			return err
		}
		op.Phase = "starting"
		if err := s.save(); err != nil {
			return err
		}
		return s.start(ctx, op, false)
	case "starting":
		return s.start(ctx, op, true)
	case "launch_exited":
		return errors.New("updater: owned candidate exited; independent bounded replacement required")
	case "checking":
		return s.check(ctx, op)
	case "rollback_stopping":
		if err := s.stopRollbackCandidate(ctx, op); err != nil {
			return err
		}
		op.Phase = "restoring"
		return s.save()
	case "restoring":
		noLaunch := op.CandidateNotStarted && op.CandidateProcess.PID == 0
		if op.Irreversible || s.hasCommittedProof(op) || (!noLaunch && (op.RollbackAuthorization == nil || !validRollbackAuthorization(op, *op.RollbackAuthorization))) {
			return errors.New("updater: restore lacks persistent admission fence")
		}
		if !noLaunch {
			if err := restoreData(s.cfg, op); err != nil {
				return err
			}
		}
		if err := s.restoreConfiguration(op.Configuration); err != nil {
			return err
		}
		op.Phase = "rollback_starting"
		launch, err := s.opts.NewID()
		if err != nil {
			return err
		}
		op.LaunchID = launch
		if err := s.save(); err != nil {
			return err
		}
		return s.start(ctx, op, false)
	case "rollback_ready":
		op.Phase = "rollback_starting"
		if err := s.save(); err != nil {
			return err
		}
		return s.start(ctx, op, false)
	case "rollback_starting":
		return s.start(ctx, op, true)
	default:
		return errors.New("updater: invalid durable operation phase")
	}
}
func (s *Supervisor) start(ctx context.Context, op *Operation, recovering bool) error {
	if s.boundedRunnerSupervisedRestart(op) || s.boundedRunnerCancelledRestart(op) {
		if err := s.boundedRunnerRestartSourceGone(ctx, op.PreviousProcess); err != nil {
			return err
		}
	}
	rollback := op.Phase == "rollback_starting"
	v := op.Candidate
	if rollback {
		v = op.Previous
	}
	if sum, err := hashFile(v.Binary); err != nil || sum != v.Artifact.SHA256 {
		return errors.New("updater: selected executable changed")
	}
	var id Identity
	if recovering {
		if proof, err := LoadOwnedLaunchExit(s.cfg, op.LaunchID, v.Artifact.SHA256); err != nil {
			return err
		} else if proof != nil {
			if err := s.criticalState(); err != nil {
				return err
			}
			op.LaunchExit, op.Phase = proof, "launch_exited"
			return s.save()
		}
		found, err := s.opts.Process.Find(ctx, op.LaunchID, v.Artifact.SHA256)
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return ErrUnknown
		}
		id = found[0]
	} else {
		if s.state.Format < 5 {
			if err := s.criticalState(); err != nil {
				return err
			}
			if err := s.save(); err != nil {
				return err
			}
		}
		extra := map[string]string{"TEND_UPDATER_INSTANCE_ID": s.cfg.InstanceID, "TEND_UPDATER_PROCESS_INSTANCE_ID": op.LaunchID, "TEND_UPDATER_LAUNCH_ID": op.LaunchID, "TEND_RELEASE_SHA256": v.Artifact.SHA256, "TEND_UPDATER_RELEASE_ID": v.ReleaseID}
		if op.Kind == "upgrade" {
			extra["TEND_UPGRADE_ID"] = op.ID
			extra["TEND_UPDATER_MAINTENANCE"] = "1"
		}
		var err error
		id, err = s.opts.Process.Start(ctx, v.Binary, s.cfg, extra)
		if err != nil {
			var exited *launchExitedError
			if errors.As(err, &exited) {
				op.LaunchExit, op.Phase = &exited.proof, "launch_exited"
				if err := s.criticalState(); err != nil {
					return err
				}
				if saveErr := s.save(); saveErr != nil {
					return saveErr
				}
			}
			if errors.Is(err, ErrNotStarted) {
				if rollback {
					op.Phase = "rollback_ready"
				}
				if op.Kind == "upgrade" && !rollback {
					op.CandidateNotStarted = true
					op.Phase = "restoring"
				} else if !rollback {
					op.Phase = "backed_up"
				}
				if saveErr := s.save(); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
	}
	if id.SHA256 != v.Artifact.SHA256 || id.LaunchID != op.LaunchID {
		return ErrUnknown
	}
	if rollback {
		s.state.Current = v
		s.state.Process = id
		op.Phase = "rolled_back"
		return s.save()
	}
	if proof := s.state.CommittedProof; op.Kind == "restart" && proof != nil && proof.Version == op.Previous && op.Candidate == op.Previous && proof.Process == op.PreviousProcess {
		proof.Process = id
	}
	if proof := s.state.RollbackProof; op.Kind == "restart" && proof != nil && proof.Version == op.Previous && op.Candidate == op.Previous && proof.Process == op.PreviousProcess {
		proof.Process = id
	}
	op.CandidateProcess = id
	op.HealthStartedAt = s.opts.Now()
	op.Phase = "checking"
	s.state.Process = id
	if op.Kind == "restart" {
		s.state.Current = v
		op.Phase = "committed"
	}
	return s.save()
}
func (s *Supervisor) check(ctx context.Context, op *Operation) (result error) {
	if err := s.ensureHealthWindow(op); err != nil {
		return err
	}
	defer func() { result = s.finishHealthRead(op, result) }()
	readCtx, cancel, err := s.healthReadContext(ctx, op)
	if err != nil {
		return err
	}
	defer cancel()
	ctx = readCtx
	if proof, err := LoadOwnedLaunchExit(s.cfg, op.LaunchID, op.Candidate.Artifact.SHA256); err != nil {
		return err
	} else if proof != nil {
		if op.CandidateProcess.PID != proof.PID || op.CandidateProcess.LaunchID != proof.LaunchID || op.CandidateProcess.SHA256 != proof.ArtifactSHA256 {
			return ErrUnknown
		}
		op.LaunchExit, op.Phase = proof, "launch_exited"
		if err := s.save(); err != nil {
			return err
		}
		return errors.New("updater: original owned candidate exited; independent bounded replacement required")
	}
	if s.opts.Authority == nil {
		return errors.New("updater: authority unavailable")
	}
	h, err := s.opts.Authority.Health(ctx, op.Binding)
	if err != nil {
		return err
	}
	if h.Binding != op.Binding || !h.ServerConfirmed || h.ServerRevision <= 0 {
		return errors.New("updater: candidate health is not server-confirmed")
	}
	if h.NewWorkAccepted && !op.Irreversible {
		op.Irreversible = true
		if err := s.save(); err != nil {
			return err
		}
	}
	id, processErr := s.opts.Process.Inspect(ctx, op.CandidateProcess.PID)
	running := processErr == nil && id == op.CandidateProcess && id.SHA256 == op.Candidate.Artifact.SHA256
	if processErr != nil && !errors.Is(processErr, ErrNotRunning) {
		return processErr
	}
	if running && h.Healthy && op.Configuration != nil {
		sum, err := configurationDigestContext(ctx, s.cfg.ServiceConfig)
		if err != nil || sum != op.Configuration.Target.SHA256 {
			return errors.New("updater: candidate configuration changed before commitment")
		}
	}
	if running && h.Healthy && h.HeartbeatSequence > op.LastHealthSequence {
		op.HealthCount++
		op.LastHealthSequence = h.HeartbeatSequence
		if op.HealthCount >= 2 {
			s.state.Current = op.Candidate
			s.state.Process = op.CandidateProcess
			op.Irreversible = true
			op.Phase = "committed"
		}
		return s.save()
	}
	if (!h.Healthy || !running) && op.HealthCount != 0 {
		op.HealthCount = 0
		if err := s.save(); err != nil {
			return err
		}
	}
	timedOut := s.opts.Now().Sub(op.HealthStartedAt) >= time.Duration(s.cfg.HealthTimeoutSeconds)*time.Second
	if (!running || timedOut) && !op.Irreversible && validRollbackAuthorization(op, h) {
		authorization := h
		op.RollbackAuthorization = &authorization
		op.Phase = "rollback_stopping"
		return s.save()
	}
	if !running || timedOut {
		return errors.New("updater: candidate requires coordinator reconciliation; rollback safety unconfirmed")
	}
	return nil
}
func (s *Supervisor) monitor(ctx context.Context) error {
	if s.state.Current.Binary == "" {
		return nil
	}
	current, err := s.observeCurrent(ctx, s.state.Process)
	if err == nil {
		if current != s.state.Process {
			return ErrUnknown
		}
		return nil
	}
	if !errors.Is(err, ErrNotRunning) {
		return err
	}
	bounded := s.boundedRunnerCommittedScope()
	cancelled := s.boundedRunnerCancelledOwnedScope()
	cancelledRestart := s.boundedRunnerCancelledRestart(s.state.Operation)
	if bounded || cancelled || cancelledRestart {
		if err := s.verifyBoundedRunnerSourceGone(ctx, s.state.Process); err != nil {
			return err
		}
	}
	// A normal crash restarts the committed binary, never a prior data snapshot.
	id, err := s.opts.NewID()
	if err != nil {
		return err
	}
	binding := Binding{}
	if bounded || cancelled || cancelledRestart {
		if proof := s.state.CommittedProof; proof != nil && proof.Version == s.state.Current && proof.Process == s.state.Process {
			binding = proof.Binding
		}
		if original := cloneState(s.state).Operation; original != nil {
			if prior, exists := s.state.OperationHistory[original.ID]; exists && digest(prior) != digest(original) {
				return errors.New("updater: original supervised operation has conflicting archived facts")
			}
			if s.state.OperationHistory == nil {
				s.state.OperationHistory = map[string]Operation{}
			}
			s.state.OperationHistory[original.ID] = *original
		}
		if cancelled {
			original := cloneState(s.state).Bounded
			key := boundedRunnerRecoveryID(original)
			if prior, exists := s.state.BoundedHistory[key]; exists && digest(prior) != digest(original) {
				return errors.New("updater: original cancellation has conflicting archived facts")
			}
			if s.state.BoundedHistory == nil {
				s.state.BoundedHistory = map[string]protocol.BoundedMaintenanceState{}
			}
			s.state.BoundedHistory[key] = *original
			s.state.Bounded, s.state.BoundedRunnerGrant = nil, nil
		}
	}
	s.state.Operation = &Operation{ID: "restart-" + id, Kind: "restart", Binding: binding, Previous: s.state.Current, PreviousProcess: s.state.Process, Candidate: s.state.Current, Phase: "backed_up", LaunchID: id, StartedAt: s.opts.Now(), BoundedSourceOwned: cancelled || cancelledRestart}
	return s.save()
}

// recoverUnlaunched restores availability without copying database snapshots:
// these durable phases prove only Stop/backup occurred, never candidate Start.
func (s *Supervisor) recoverUnlaunched(ctx context.Context, op *Operation) error {
	if op.Kind != "upgrade" || op.Irreversible || op.CandidateProcess.PID != 0 || (op.Phase != "stopped" && op.Phase != "backed_up" && op.Phase != "config_applying") {
		return ErrUnknown
	}
	if err := s.restoreConfiguration(op.Configuration); err != nil {
		return err
	}
	launch, err := s.opts.NewID()
	if err != nil {
		return err
	}
	op.LaunchID = launch
	op.Phase = "rollback_starting"
	if err := s.save(); err != nil {
		return err
	}
	return s.start(ctx, op, false)
}

func (s *Supervisor) recoverStopping(ctx context.Context, op *Operation) error {
	if op.Kind != "upgrade" || op.Irreversible || op.CandidateProcess.PID != 0 {
		return ErrUnknown
	}
	id, err := s.opts.Process.Inspect(ctx, op.PreviousProcess.PID)
	if errors.Is(err, ErrNotRunning) {
		// A crashed Stop may have completed before its phase was saved.
		op.Phase = "stopped"
		if err := s.save(); err != nil {
			return err
		}
		return s.recoverUnlaunched(ctx, op)
	}
	if err != nil {
		return err
	}
	if id != op.PreviousProcess || id.LaunchID == "" {
		return ErrUnknown
	}
	// Expired authorization cannot stop a still-running original process.
	// Retain it and report a terminal, identity-bound no-replacement outcome.
	s.state.Current = op.Previous
	s.state.Process = id
	op.LaunchID = id.LaunchID
	op.Phase = "rolled_back"
	return s.save()
}

// Cancelled requires a durable terminal cancellation, never absence of work.
func Cancelled(state State, id string) bool {
	receipt, ok := state.Terminals[id]
	return ok && receipt.Phase == "cancelled" && receipt.Binding.UpgradeID == id && receipt.Binding.InstanceID == state.InstanceID
}

func validRollbackAuthorization(op *Operation, h Health) bool {
	return h.Binding == op.Binding && h.ServerConfirmed && h.ServerRevision > 0 && h.ControlRevision > 0 && h.AdmissionFenced && h.RollbackSafe && !h.NewWorkAccepted
}
func (s *Supervisor) hasCommittedProof(op *Operation) bool {
	return s.state.CommittedProof != nil && s.state.CommittedProof.Binding == op.Binding
}
func (s *Supervisor) stopRollbackCandidate(ctx context.Context, op *Operation) error {
	if op.Irreversible || s.hasCommittedProof(op) || op.RollbackAuthorization == nil || !validRollbackAuthorization(op, *op.RollbackAuthorization) {
		return errors.New("updater: rollback lacks persistent admission fence")
	}
	id, err := s.opts.Process.Inspect(ctx, op.CandidateProcess.PID)
	if errors.Is(err, ErrNotRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	if id != op.CandidateProcess {
		return ErrUnknown
	}
	if s.opts.Authority == nil {
		return errors.New("updater: rollback authority unavailable")
	}
	h, err := s.opts.Authority.Health(ctx, op.Binding)
	if err != nil {
		return err
	}
	if h.Binding == op.Binding && h.ServerConfirmed && h.ServerRevision > 0 && h.NewWorkAccepted {
		op.Irreversible = true
		if err := s.save(); err != nil {
			return err
		}
	}
	if op.Irreversible || !validRollbackAuthorization(op, h) || h.ControlRevision < op.RollbackAuthorization.ControlRevision || h.ServerRevision < op.RollbackAuthorization.ServerRevision {
		return errors.New("updater: rollback admission fence changed")
	}
	authorization := h
	op.RollbackAuthorization = &authorization
	if err := s.save(); err != nil {
		return err
	}
	return s.opts.Process.Stop(ctx, op.CandidateProcess, time.Duration(s.cfg.StopTimeoutSeconds)*time.Second)
}

// observeCurrent caches a full verification only for this exact live executable.
// A new supervisor always verifies before trusting its first cheap observation.
func (s *Supervisor) observeCurrent(ctx context.Context, expected Identity) (Identity, error) {
	observer, ok := s.opts.Process.(ProcessObserver)
	if !ok {
		return s.opts.Process.Inspect(ctx, expected.PID)
	}
	observed, err := observer.Observe(ctx, expected.PID)
	if err != nil {
		return Identity{}, err
	}
	if s.verifiedProcess == expected && s.observedProcess == observed {
		return expected, nil
	}
	full, err := s.opts.Process.Inspect(ctx, expected.PID)
	if err != nil {
		return Identity{}, err
	}
	if full != expected {
		return Identity{}, ErrUnknown
	}
	again, err := observer.Observe(ctx, expected.PID)
	if err != nil {
		return Identity{}, err
	}
	light := full
	light.SHA256 = ""
	if observed != again || again.Identity != light {
		return Identity{}, ErrUnknown
	}
	s.verifiedProcess = full
	s.observedProcess = again
	return full, nil
}
