package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/release"
)

type Options struct {
	Process   Process
	Authority Authority
	Now       func() time.Time
	NewID     func() (string, error)
	BootClock func() (string, int64, error)
}
type Supervisor struct {
	durable        State
	persistenceErr error
	writeState     func(string, State) error
	mu             sync.Mutex
	cfg            Config
	opts           Options
	state          State
	lock           *os.File
}

func Open(cfg Config, opts Options) (*Supervisor, error) {
	if err := supported(); err != nil {
		return nil, err
	}
	if cfg.InstanceID == "" || len(cfg.InstanceID) > 256 || cfg.Role != "server" && cfg.Role != "runner" || !filepath.IsAbs(cfg.StateDir) || !filepath.IsAbs(cfg.InitialBinary) || !filepath.IsAbs(cfg.LogPath) {
		return nil, errors.New("updater: invalid service configuration")
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
	lock, err := lockDir(cfg.StateDir)
	if err != nil {
		return nil, err
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
	if state.Format != 1 || state.InstanceID != cfg.InstanceID {
		return fail(errors.New("updater: durable state belongs to a different instance"))
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
	if s.persistenceErr != nil {
		return fmt.Errorf("updater: durability unknown; reopen supervisor before further action: %w", s.persistenceErr)
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
	if err := s.writeState(s.statePath(), cloneState(s.state)); err != nil {
		s.persistenceErr = err
		return s.durabilityError()
	}
	s.durable = cloneState(s.state)
	return nil
}
func cloneState(v State) State { // no aliases to internal mutable operation state
	if v.CommittedProof != nil {
		proof := *v.CommittedProof
		v.CommittedProof = &proof
	}
	if v.RollbackProof != nil {
		proof := *v.RollbackProof
		v.RollbackProof = &proof
	}
	if v.Terminals != nil {
		cloned := make(map[string]TerminalReceipt, len(v.Terminals))
		for id, r := range v.Terminals {
			cloned[id] = r
		}
		v.Terminals = cloned
	}
	if v.Operation != nil {
		o := *v.Operation
		if o.RollbackAuthorization != nil {
			auth := *o.RollbackAuthorization
			o.RollbackAuthorization = &auth
		}
		o.Backups = append([]BackupFile(nil), o.Backups...)
		if o.Grant != nil {
			g := *o.Grant
			o.Grant = &g
		}
		v.Operation = &o
	}
	return v
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
	if !release.ValidID(id) {
		return errors.New("updater: invalid upgrade identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if s.state.Current.Binary == "" {
		return errors.New("updater: bootstrap required")
	}
	if receipt, ok := s.state.Terminals[id]; ok {
		if receipt.Phase == "cancelled" {
			return errors.New("updater: operation was cancelled")
		}
		bundle, err := release.Load(dir)
		if err != nil {
			return err
		}
		if receipt.Binding.ManifestSHA256 != bundle.ManifestSHA256 {
			return errors.New("updater: upgrade identity conflicts with historical payload")
		}
		return nil
	}
	candidate, err := s.stage(dir)
	if err != nil {
		return err
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
	s.state.Operation = &Operation{ID: id, Kind: "upgrade", Binding: b, Previous: s.state.Current, Candidate: candidate, PreviousProcess: s.state.Process, LaunchID: processID, Phase: "staged", StartedAt: s.opts.Now()}
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
	if err := s.durabilityError(); err != nil {
		return err
	}
	if !release.ValidID(id) {
		return errors.New("updater: invalid operation identity")
	}
	op := s.state.Operation
	if expectedPhase == "staged" && (op == nil || op.ID != id) {
		if receipt, ok := s.state.Terminals[id]; ok && receipt.Phase != "cancelled" {
			return errors.New("updater: historical operation already completed")
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
	if err := s.durabilityError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
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
	if err != nil {
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
		if err := s.authorize(ctx, op); err != nil {
			return err
		}
		// Equal bytes skip process replacement; authorization is still checked and
		// the manifest identity is preserved as the selected release.
		if op.Candidate.Artifact.SHA256 == s.state.Current.Artifact.SHA256 {
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
			if err := backupData(s.cfg, op); err != nil {
				return err
			}
		}
		op.Phase = "backed_up"
		return s.save()
	case "backed_up":
		if err := s.grantValid(op); err != nil {
			return s.recoverUnlaunched(ctx, op)
		}
		op.Phase = "starting"
		if err := s.save(); err != nil {
			return err
		}
		return s.start(ctx, op, false)
	case "starting":
		return s.start(ctx, op, true)
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
		found, err := s.opts.Process.Find(ctx, op.LaunchID, v.Artifact.SHA256)
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return ErrUnknown
		}
		id = found[0]
	} else {
		extra := map[string]string{"TEND_UPDATER_INSTANCE_ID": s.cfg.InstanceID, "TEND_UPDATER_PROCESS_INSTANCE_ID": op.LaunchID, "TEND_UPDATER_LAUNCH_ID": op.LaunchID, "TEND_RELEASE_SHA256": v.Artifact.SHA256, "TEND_UPDATER_RELEASE_ID": v.ReleaseID}
		if op.Kind == "upgrade" {
			extra["TEND_UPGRADE_ID"] = op.ID
			extra["TEND_UPDATER_MAINTENANCE"] = "1"
		}
		var err error
		id, err = s.opts.Process.Start(ctx, v.Binary, s.cfg, extra)
		if err != nil {
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
func (s *Supervisor) check(ctx context.Context, op *Operation) error {
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
	running := processErr == nil && id.StartTicks == op.CandidateProcess.StartTicks && id.SHA256 == op.Candidate.Artifact.SHA256
	if processErr != nil && !errors.Is(processErr, ErrNotRunning) {
		return processErr
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
	current, err := s.opts.Process.Inspect(ctx, s.state.Process.PID)
	if err == nil {
		if current.StartTicks != s.state.Process.StartTicks || current.SHA256 != s.state.Process.SHA256 {
			return ErrUnknown
		}
		return nil
	}
	if !errors.Is(err, ErrNotRunning) {
		return err
	}
	// A normal crash restarts the committed binary, never a prior data snapshot.
	id, err := s.opts.NewID()
	if err != nil {
		return err
	}
	s.state.Operation = &Operation{ID: "restart-" + id, Kind: "restart", Previous: s.state.Current, PreviousProcess: s.state.Process, Candidate: s.state.Current, Phase: "backed_up", LaunchID: id, StartedAt: s.opts.Now()}
	return s.save()
}

// recoverUnlaunched restores availability without copying database snapshots:
// these durable phases prove only Stop/backup occurred, never candidate Start.
func (s *Supervisor) recoverUnlaunched(ctx context.Context, op *Operation) error {
	if op.Kind != "upgrade" || op.Irreversible || op.CandidateProcess.PID != 0 || (op.Phase != "stopped" && op.Phase != "backed_up") {
		return ErrUnknown
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
