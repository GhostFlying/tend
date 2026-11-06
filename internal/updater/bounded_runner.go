package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// AuthorizeBoundedRunnerMaintenance accepts a separately issued owner grant.
// Neither an ordinary upgrade grant nor the runner's readiness authorizes it.
func (s *Supervisor) AuthorizeBoundedRunnerMaintenance(ctx context.Context, grant protocol.BoundedRunnerMaintenanceGrant, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	p, a := grant.Plan, grant.Plan.Authorization
	planSHA, err := protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		return err
	}
	if s.cfg.Role != "runner" || p.HostID != s.cfg.InstanceID || a.Role != "runner" || a.Target != p.Target || a.Source != p.Source || a.Target.InstanceID != s.cfg.InstanceID || a.PlanSHA256 != planSHA || !protocol.ValidID(protocol.PrefixRequest, a.RequestID) || a.Reason == "" || a.DataIdentity == nil || *a.DataIdentity != p.Journal || grant.ControlRevision <= p.ExpectedControlRevision || !release.ValidSHA(grant.ProtectionSetSHA256) {
		return errors.New("updater: independent exact runner owner grant required")
	}
	if grant.RecoveryAuthorizationID == "" {
		return errors.New("updater: stable runner recovery authorization required")
	}
	if !release.ValidID(p.Target.UpgradeID) || !release.ValidID(p.Target.ReleaseID) || !release.ValidSHA(p.Target.ManifestSHA256) || !release.ValidSHA(p.Target.ArtifactSHA256) || p.Target.ProcessInstanceID == "" || p.Target.ProcessInstanceID == p.Source.LaunchID {
		return errors.New("updater: complete independent runner successor required")
	}
	for _, history := range s.state.BoundedHistory {
		if original, exists := history.Authorizations[a.RequestID]; exists {
			previous, known := s.state.BoundedRunnerGrants[a.RequestID]
			if action == "cancel" && history.Outcome == "cancelled" && digest(original) == digest(a) && known && digest(previous) == digest(grant) {
				for _, entry := range history.Audit {
					if entry.RequestID == a.RequestID && entry.Action == "cancel" && entry.Outcome == "cancelled_before_stop" {
						return nil
					}
				}
			}
			return errors.New("updater: historical runner request requires its original action and payload")
		}
	}
	for _, history := range s.state.BoundedHistory {
		if history.Authorization.PlanSHA256 == a.PlanSHA256 && history.Outcome == "cancelled" {
			return errors.New("updater: cancelled runner maintenance requires a new source plan")
		}
	}
	if s.state.Bounded == nil && grant.RecoveryAuthorizationID != a.RequestID {
		return errors.New("updater: initial runner recovery lineage changed")
	}
	if b := s.state.Bounded; b != nil && b.Authorization.PlanSHA256 == a.PlanSHA256 && grant.RecoveryAuthorizationID != boundedRunnerRecoveryID(b) {
		return errors.New("updater: original runner recovery lineage changed")
	}
	if b := s.state.Bounded; b != nil && b.Authorization.PlanSHA256 != a.PlanSHA256 && grant.RecoveryAuthorizationID != a.RequestID {
		return errors.New("updater: new runner source plan requires a new recovery lineage")
	}
	if err := validateTimeoutPolicy(a.Timeouts); err != nil {
		return err
	}
	if _, err := retainedRunIdentities(grant.Runs); err != nil {
		return err
	}
	for _, run := range grant.Runs {
		if run.Run.Host != p.HostID {
			return errors.New("updater: runner grant includes another host")
		}
	}
	if b := s.state.Bounded; b != nil {
		if old, ok := b.Authorizations[a.RequestID]; ok {
			previous, known := s.state.BoundedRunnerGrants[a.RequestID]
			if !known && s.state.BoundedRunnerGrant != nil {
				previous = *s.state.BoundedRunnerGrant
				known = true
			}
			if digest(old) != digest(a) || !known || digest(previous) != digest(grant) {
				return errors.New("updater: bounded runner request payload changed")
			}
			for _, entry := range b.Audit {
				if entry.RequestID == a.RequestID {
					if entry.Action != action {
						return errors.New("updater: bounded runner request action changed")
					}
					break
				}
			}
			return nil
		}
	}
	phase := "idle"
	if s.state.Operation != nil {
		phase = s.state.Operation.Phase
	}
	if s.state.Bounded != nil {
		phase = s.state.Bounded.Phase
	}
	if !s.boundedGenerationMatches(a.ExpectedGeneration) || a.ExpectedPhase != phase {
		return errors.New("updater: bounded runner generation or phase changed")
	}
	if a.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
		return errors.New("updater: bounded runner authority expired")
	}
	if action != "apply" && action != "force" && action != "takeover" && action != "extend" && action != "cancel" {
		return errors.New("updater: unsupported bounded runner action")
	}
	if action == "cancel" {
		if s.state.Bounded == nil || s.state.Bounded.SourceExit != nil || s.state.Operation == nil || s.state.Operation.Phase != "staged" {
			return errors.New("updater: stopped or launched runner requires explicit forward recovery")
		}
		s.state.Bounded.Authorizations[a.RequestID] = a
		if s.state.BoundedRunnerGrants == nil {
			s.state.BoundedRunnerGrants = map[string]protocol.BoundedRunnerMaintenanceGrant{}
		}
		s.state.BoundedRunnerGrants[a.RequestID] = grant
		s.state.Bounded.Audit = append(s.state.Bounded.Audit, protocol.BoundedMaintenanceAudit{RequestID: a.RequestID, Action: action, Phase: phase, Reason: a.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "cancelled_before_stop"})
		s.state.Bounded.Outcome = "cancelled"
		s.state.Operation.Phase = "cancelled"
		return s.save()
	}
	if b := s.state.Bounded; b != nil && b.Authorization.PlanSHA256 == a.PlanSHA256 && s.state.Operation != nil && s.state.Operation.Phase == "cancelled" {
		return errors.New("updater: cancelled runner maintenance requires a new source plan")
	}
	if err := validateBoundedRunnerWindow(a, s.opts.Now().UnixMilli()); err != nil {
		return err
	}
	if action == "extend" {
		b := s.state.Bounded
		if b == nil || s.state.BoundedRunnerGrant == nil || b.Authorization.PlanSHA256 != a.PlanSHA256 || b.Authorization.Source != a.Source || b.Authorization.Target != a.Target ||
			digest(grant.Runs) != digest(s.state.BoundedRunnerGrant.Runs) || grant.ProtectionSetSHA256 != s.state.BoundedRunnerGrant.ProtectionSetSHA256 {
			return errors.New("updater: extension must preserve the original runner source plan")
		}
		boot, mono, err := s.opts.BootClock()
		if err != nil {
			return err
		}
		now := s.opts.Now().UnixMilli()
		if err := validateBoundedRunnerWindow(a, now); err != nil {
			return err
		}
		b.Authorizations[a.RequestID] = a
		b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: a.RequestID, Action: action, Phase: b.Phase, Reason: a.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "fresh_external_window", PreviousWindow: boundedWindowSnapshot(b)})
		b.Authorization = a
		b.BootID = boot
		b.OverallStartedAtMillis = now
		b.OverallDeadlineMillis = now + a.Timeouts.OverallMillis
		b.OverallDeadlineMono = mono + a.Timeouts.OverallMillis
		budget := boundedRunnerPhaseBudget(b.Phase, a.Timeouts)
		b.PhaseEnteredAtMillis = now
		b.PhaseDeadlineMillis = min(now+budget, b.OverallDeadlineMillis)
		b.PhaseDeadlineMono = min(mono+budget, b.OverallDeadlineMono)
		s.state.BoundedRunnerGrant = &grant
		if s.state.BoundedRunnerGrants == nil {
			s.state.BoundedRunnerGrants = map[string]protocol.BoundedRunnerMaintenanceGrant{}
		}
		s.state.BoundedRunnerGrants[a.RequestID] = grant
		return s.save()
	}
	if b := s.state.Bounded; b != nil && s.state.BoundedRunnerGrant != nil && b.Authorization.PlanSHA256 == a.PlanSHA256 {
		if digest(grant.Runs) != digest(s.state.BoundedRunnerGrant.Runs) || grant.ProtectionSetSHA256 != s.state.BoundedRunnerGrant.ProtectionSetSHA256 {
			return errors.New("updater: original bounded runner protection set changed")
		}
		if s.state.Operation == nil || s.state.Operation.Phase == "committed" {
			return errors.New("updater: installed runner requires a new successor source plan")
		}
		boot, mono, err := s.opts.BootClock()
		if err != nil {
			return err
		}
		now := s.opts.Now().UnixMilli()
		if err := validateBoundedRunnerWindow(a, now); err != nil {
			return err
		}
		previousWindow := boundedWindowSnapshot(b)
		b.Authorizations[a.RequestID] = a
		b.Authorization = a
		b.BootID = boot
		b.OverallStartedAtMillis = now
		b.OverallDeadlineMillis = now + a.Timeouts.OverallMillis
		b.OverallDeadlineMono = mono + a.Timeouts.OverallMillis
		budget := boundedRunnerPhaseBudget(b.Phase, a.Timeouts)
		b.PhaseEnteredAtMillis = now
		b.PhaseDeadlineMillis = min(now+budget, b.OverallDeadlineMillis)
		b.PhaseDeadlineMono = min(mono+budget, b.OverallDeadlineMono)
		b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: a.RequestID, Action: action, Phase: b.Phase, Reason: a.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "original_launch_and_unknown_work_retained", PreviousWindow: previousWindow})
		s.state.BoundedRunnerGrant = &grant
		if s.state.BoundedRunnerGrants == nil {
			s.state.BoundedRunnerGrants = map[string]protocol.BoundedRunnerMaintenanceGrant{}
		}
		s.state.BoundedRunnerGrants[a.RequestID] = grant
		if (action == "force" || action == "takeover") && s.state.Operation.Phase == "staged" {
			s.state.Operation.Phase = "stopping"
			b.Phase = ""
			return s.boundedEnter("stop", a.Timeouts.StopMillis)
		}
		return s.save()
	}
	if err := s.validateBoundedRunnerData(p.Journal); err != nil {
		return err
	}
	actual, inspectErr := s.opts.Process.Inspect(ctx, p.Source.PID)
	if inspectErr != nil && !errors.Is(inspectErr, ErrNotRunning) {
		return inspectErr
	}
	if inspectErr == nil && actual != p.Source {
		return errors.New("updater: bounded runner source process changed")
	}
	if !validIdentity(p.Source) {
		return errors.New("updater: bounded runner source identity is incomplete")
	}
	if prior := s.state.Operation; prior != nil && !done(prior) && (prior.Phase == "starting" || prior.Phase == "checking") {
		if !release.ValidSHA(prior.Candidate.Artifact.SHA256) || prior.LaunchID == "" {
			return errors.New("updater: unfinished predecessor launch requires external identity reconciliation")
		}
		found, err := s.opts.Process.Find(ctx, prior.LaunchID, prior.Candidate.Artifact.SHA256)
		if err != nil {
			return err
		}
		for _, candidate := range found {
			if candidate != p.Source {
				return errors.New("updater: runner source plan omits a live predecessor candidate writer")
			}
		}
	}
	previous := s.state.Current
	if op := s.state.Operation; op != nil && (op.CandidateProcess == p.Source || op.Candidate.Artifact.SHA256 == p.Source.SHA256 && op.LaunchID == p.Source.LaunchID) {
		previous = op.Candidate
	}
	if previous.Binary == "" {
		sha, err := hashFile(s.cfg.InitialBinary)
		if err != nil || sha != p.Source.SHA256 {
			return errors.New("updater: manual runner adoption source binary changed")
		}
		previous = Version{Binary: s.cfg.InitialBinary, Protocol: s.cfg.Protocol, StorageVersion: p.Journal.Version, Artifact: release.Artifact{Role: "runner", SHA256: sha}}
	} else if previous.Artifact.SHA256 != p.Source.SHA256 {
		return errors.New("updater: bounded runner source differs from installed bytes")
	}
	previous.StorageVersion = p.Journal.Version
	originalState := cloneState(s.state)
	originalOperation := originalState.Operation
	sourceSupervised := originalState.Process == p.Source && originalState.Current == previous && done(originalOperation)
	if originalState.Bounded != nil && originalOperation != nil && originalOperation.Phase == "cancelled" {
		sourceSupervised = sourceSupervised && originalOperation.BoundedSourceOwned
	}
	if originalOperation != nil {
		if prior, exists := s.state.OperationHistory[originalOperation.ID]; exists && digest(prior) != digest(*originalOperation) {
			return errors.New("updater: original operation identity already has different archived facts")
		}
	}
	if old := originalState.Bounded; old != nil {
		if prior, exists := s.state.BoundedHistory[boundedRunnerRecoveryID(old)]; exists && digest(prior) != digest(old) {
			return errors.New("updater: original bounded window already has different archived facts")
		}
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	now := s.opts.Now().UnixMilli()
	if err := validateBoundedRunnerWindow(a, now); err != nil {
		return err
	}
	b := &protocol.BoundedMaintenanceState{RecoveryAuthorizationID: grant.RecoveryAuthorizationID, Authorization: a, Authorizations: map[string]protocol.BoundedMaintenanceAuthorization{a.RequestID: a}, Phase: "handoff", PhaseEnteredAtMillis: now, PhaseDeadlineMillis: now + a.Timeouts.HandoffMillis, OverallStartedAtMillis: now, OverallDeadlineMillis: now + a.Timeouts.OverallMillis, BootID: boot, PhaseDeadlineMono: mono + a.Timeouts.HandoffMillis, OverallDeadlineMono: mono + a.Timeouts.OverallMillis}
	if b.RecoveryAuthorizationID == "" {
		b.RecoveryAuthorizationID = a.RequestID
	}
	if old := originalState.Bounded; old != nil {
		if s.state.BoundedHistory == nil {
			s.state.BoundedHistory = map[string]protocol.BoundedMaintenanceState{}
		}
		s.state.BoundedHistory[boundedRunnerRecoveryID(old)] = *old
	}
	b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: a.RequestID, Action: action, Phase: "handoff", Reason: a.Reason, RecordedAtMillis: now, Outcome: "unknown_work_retained"})
	s.state.Current = previous
	s.state.Bounded = b
	s.state.BoundedRunnerGrant = &grant
	if s.state.BoundedRunnerGrants == nil {
		s.state.BoundedRunnerGrants = map[string]protocol.BoundedRunnerMaintenanceGrant{}
	}
	s.state.BoundedRunnerGrants[a.RequestID] = grant
	s.state.Format = 4
	if s.state.UpdaterEpoch == "" {
		epoch, err := s.opts.NewID()
		if err != nil {
			return err
		}
		s.state.UpdaterEpoch = epoch
		s.state.UpdaterBootID = boot
	}
	if original := originalOperation; original != nil {
		if s.state.OperationHistory == nil {
			s.state.OperationHistory = map[string]Operation{}
		}
		s.state.OperationHistory[original.ID] = *original
	}
	s.state.Operation = &Operation{ID: p.Target.UpgradeID, Kind: "upgrade", Binding: p.Target, Previous: previous, PreviousProcess: p.Source, LaunchID: p.Target.ProcessInstanceID, Phase: "bounded_staging", StartedAt: s.opts.Now(), BoundedSourceOwned: sourceSupervised}
	if err := s.save(); err != nil {
		return err
	}
	candidate, err := s.stage(p.ReleaseDir)
	if err != nil {
		return err
	}
	if candidate.ManifestSHA256 != p.Target.ManifestSHA256 || candidate.Artifact.SHA256 != p.Target.ArtifactSHA256 || candidate.ReleaseID != p.Target.ReleaseID {
		return errors.New("updater: bounded runner candidate differs from owner target")
	}
	if candidate.StorageVersion < 17 {
		return errors.New("updater: bounded runner candidate lacks durable quarantine journal support")
	}
	s.state.Operation.Candidate = candidate
	s.state.Operation.Phase = "staged"
	if action == "force" || action == "takeover" {
		s.state.Operation.Phase = "stopping"
		return s.boundedEnter("stop", a.Timeouts.StopMillis)
	}
	return s.boundedEnter("observation_drain", a.Timeouts.DrainMillis)
}

func (s *Supervisor) validateBoundedRunnerData(data protocol.BoundedDataIdentity) error {
	if data.Path == "" || !filepath.IsAbs(data.Path) || data.Inode == 0 || data.Incarnation == "" || data.Version <= 0 {
		return errors.New("updater: complete runner journal identity required")
	}
	listed := false
	for _, path := range s.cfg.DataFiles {
		if path == data.Path {
			listed = true
		}
	}
	if !listed {
		return errors.New("updater: runner journal differs from configured data")
	}
	device, inode, err := BoundedDataFileIdentity(data.Path)
	if err != nil {
		return err
	}
	if device != data.Device || inode != data.Inode {
		return errors.New("updater: runner journal inode changed")
	}
	incDevice, incInode, err := BoundedDataFileIdentity(data.Path + ".incarnation")
	if err != nil {
		return err
	}
	if data.Incarnation != fmt.Sprintf("%d:%d", incDevice, incInode) {
		return errors.New("updater: runner journal incarnation changed")
	}
	return nil
}

// boundedRunnerStep owns the complete forced runner lifecycle. It never calls
// the ordinary grant or rollback paths, and never equates Ready with health.
func (s *Supervisor) boundedRunnerStep(ctx context.Context) (handled bool, resultErr error) {
	defer func() {
		if handled && resultErr != nil && s.state.Operation != nil && s.state.Operation.LastError != resultErr.Error() {
			s.state.Operation.LastError = resultErr.Error()
			if err := s.save(); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	b, op := s.state.Bounded, s.state.Operation
	if b == nil || b.Authorization.Role != "runner" {
		return false, nil
	}
	if op == nil || s.state.BoundedRunnerGrant == nil {
		return true, errors.New("updater: bounded runner operation is missing")
	}
	if op.Kind == "restart" {
		if !s.boundedRunnerSupervisedRestart(op) {
			return true, errors.New("updater: restart changed the committed bounded lineage")
		}
		return false, nil
	}
	if op.Phase == "cancelled" {
		if !s.boundedRunnerCancelledOwnedScope() {
			return true, errors.New("updater: cancelled runner source is not supervisor-owned; an independent new source plan is required")
		}
		return false, nil
	}
	if op.Phase == "committed" {
		if !s.boundedRunnerCommittedScope() || op.Binding != s.state.CommittedProof.Binding {
			return true, errors.New("updater: committed bounded runner proof changed")
		}
		return false, nil
	}
	if op.Phase == "bounded_staging" {
		return true, errors.New("updater: staging outcome requires explicit external reconciliation")
	}
	if b.Authorization.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
		_, err := s.boundedRunnerRemaining()
		return true, err
	}
	if op.Phase == "staged" {
		remaining, err := s.boundedRunnerRemaining()
		if err != nil && !b.Authorization.ForceOnDrainTimeout {
			return true, err
		}
		if err == nil {
			if s.opts.Authority == nil {
				return true, errors.New("updater: fixed runner drain authority is unavailable")
			}
			readCtx, cancel := context.WithTimeout(ctx, min(remaining, time.Second))
			grant, observeErr := s.opts.Authority.Authorize(readCtx, op.Binding)
			cancel()
			if observeErr != nil || !grant.Allowed || grant.Binding != op.Binding {
				return true, errors.New("updater: runner drain not confirmed; bounded external force is available")
			}
		}
		if err != nil {
			b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "force_on_drain_timeout", Phase: b.Phase, Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "unknown_work_retained"})
		}
		op.Phase = "stopping"
		return true, s.boundedEnter("stop", b.Authorization.Timeouts.StopMillis)
	}
	if op.Phase == "stopped" {
		if err := s.boundedEnter("backup", b.Authorization.Timeouts.BackupMillis); err != nil {
			return true, err
		}
	}
	if op.Phase == "backed_up" || op.Phase == "starting" || op.Phase == "checking" {
		if err := s.boundedEnter("start_health", b.Authorization.Timeouts.StartHealthMillis); err != nil {
			return true, err
		}
	}
	remaining, deadlineErr := s.boundedRunnerRemaining()
	if deadlineErr != nil {
		if op.Phase == "stopping" && b.Phase == "stop" && boundedRunnerMayKill(b) {
			if err := s.boundedEnter("kill", b.Authorization.Timeouts.KillMillis); err != nil {
				return true, err
			}
			return true, s.boundedRunnerStop(ctx, true)
		}
		b.Outcome = "timed_out"
		op.LastError = deadlineErr.Error()
		if err := s.save(); err != nil {
			return true, err
		}
		return true, deadlineErr
	}
	stepCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	switch op.Phase {
	case "stopping":
		if b.Phase == "kill" && boundedRunnerMayKill(b) {
			return true, s.boundedRunnerStop(stepCtx, true)
		}
		return true, s.boundedRunnerStop(stepCtx, false)
	case "stopped":
		if b.SourceExit == nil {
			return true, errors.New("updater: predecessor exit is not confirmed")
		}
		if err := s.validateBoundedRunnerData(s.state.BoundedRunnerGrant.Plan.Journal); err != nil {
			return true, err
		}
		op.BackupDir = filepath.Join(s.cfg.StateDir, "backups", op.ID+"-"+b.Authorization.RequestID)
		if err := s.save(); err != nil {
			return true, err
		}
		if err := backupBoundedRunnerData(s.cfg, op); err != nil {
			return true, err
		}
		op.Phase = "backed_up"
		return true, s.save()
	case "backed_up":
		op.Phase = "starting"
		if err := s.save(); err != nil {
			return true, err
		}
		return true, s.startBoundedRunner(stepCtx, false)
	case "starting":
		return true, s.startBoundedRunner(stepCtx, true)
	case "checking":
		return true, s.checkBoundedRunner(stepCtx)
	default:
		return true, errors.New("updater: invalid bounded runner phase")
	}
}

func (s *Supervisor) boundedRunnerStop(ctx context.Context, kill bool) error {
	b, op := s.state.Bounded, s.state.Operation
	remaining, err := s.boundedRunnerRemaining()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	actual, err := s.opts.Process.Inspect(ctx, op.PreviousProcess.PID)
	if errors.Is(err, ErrNotRunning) {
		if err := s.verifyBoundedRunnerSourceGone(ctx, op.PreviousProcess); err != nil {
			return err
		}
		b.SourceExit = &protocol.BoundedProcessExit{Process: op.PreviousProcess, ConfirmedAtMillis: s.opts.Now().UnixMilli(), Method: "exact_process_exit"}
		op.Phase = "stopped"
		return s.save()
	}
	if err != nil {
		return err
	}
	if actual != op.PreviousProcess {
		return ErrUnknown
	}
	if b.Phase == "kill" && !kill {
		return ErrUnknown
	}
	if kill {
		crash, ok := s.opts.Process.(receiptRecoveryProcess)
		if !ok {
			return errors.New("updater: exact forced runner termination unavailable")
		}
		b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "kill_predecessor", Phase: "kill", Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "started"})
		if err := s.save(); err != nil {
			return err
		}
		remaining, err := s.boundedRunnerRemaining()
		if err != nil {
			return err
		}
		killCtx, cancel := context.WithTimeout(ctx, remaining)
		defer cancel()
		if err := crash.Crash(killCtx, op.PreviousProcess, remaining); err != nil {
			return err
		}
	} else {
		for _, entry := range b.Audit {
			previous := b.Authorizations[entry.RequestID]
			if entry.Action == "stop_predecessor" && previous.PlanSHA256 == b.Authorization.PlanSHA256 && previous.Source == b.Authorization.Source {
				return ErrUnknown
			}
		}
		b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "stop_predecessor", Phase: "stop", Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "started"})
		if err := s.save(); err != nil {
			return err
		}
		remaining, err := s.boundedRunnerRemaining()
		if err != nil {
			return err
		}
		stopCtx, cancel := context.WithTimeout(ctx, remaining)
		defer cancel()
		if err := s.opts.Process.Stop(stopCtx, op.PreviousProcess, remaining); err != nil {
			return err
		}
	}
	if _, err := s.opts.Process.Inspect(ctx, op.PreviousProcess.PID); !errors.Is(err, ErrNotRunning) {
		if err != nil {
			return err
		}
		return ErrUnknown
	}
	if err := s.verifyBoundedRunnerSourceGone(ctx, op.PreviousProcess); err != nil {
		return err
	}
	b.SourceExit = &protocol.BoundedProcessExit{Process: op.PreviousProcess, ConfirmedAtMillis: s.opts.Now().UnixMilli(), Method: "exact_process_exit"}
	op.Phase = "stopped"
	return s.save()
}

func (s *Supervisor) startBoundedRunner(ctx context.Context, recovering bool) error {
	b, op := s.state.Bounded, s.state.Operation
	if b.SourceExit == nil || b.SourceExit.Process != op.PreviousProcess {
		return errors.New("updater: old runner writer exit is not confirmed")
	}
	if _, err := s.opts.Process.Inspect(ctx, op.PreviousProcess.PID); !errors.Is(err, ErrNotRunning) {
		if err != nil {
			return err
		}
		return ErrUnknown
	}
	if err := s.verifyBoundedRunnerSourceGone(ctx, op.PreviousProcess); err != nil {
		return err
	}
	if sha, err := hashFile(op.Candidate.Binary); err != nil || sha != op.Candidate.Artifact.SHA256 {
		return errors.New("updater: bounded runner candidate bytes changed")
	}
	var identity Identity
	if recovering {
		found, err := s.opts.Process.Find(ctx, op.LaunchID, op.Candidate.Artifact.SHA256)
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return ErrUnknown
		}
		identity = found[0]
	} else {
		remaining, err := s.boundedRunnerRemaining()
		if err != nil {
			return err
		}
		startCtx, cancel := context.WithTimeout(ctx, remaining)
		defer cancel()
		extra := map[string]string{"TEND_UPDATER_INSTANCE_ID": s.cfg.InstanceID, "TEND_UPDATER_PROCESS_INSTANCE_ID": op.LaunchID, "TEND_UPDATER_LAUNCH_ID": op.LaunchID, "TEND_RELEASE_SHA256": op.Candidate.Artifact.SHA256, "TEND_UPDATER_RELEASE_ID": op.Candidate.ReleaseID, "TEND_UPGRADE_ID": op.ID, "TEND_UPDATER_MAINTENANCE": "1", "TEND_BOUNDED_RECOVERY_REQUEST_ID": boundedRunnerRecoveryID(b)}
		identity, err = s.opts.Process.Start(startCtx, op.Candidate.Binary, s.cfg, extra)
		if err != nil {
			return err
		}
	}
	if identity.LaunchID != op.LaunchID || identity.SHA256 != op.Candidate.Artifact.SHA256 {
		return ErrUnknown
	}
	op.CandidateProcess = identity
	op.HealthStartedAt = s.opts.Now()
	op.Phase = "checking"
	op.Irreversible = true
	s.state.Process = identity
	return s.save()
}

func validateBoundedRunnerWindow(a protocol.BoundedMaintenanceAuthorization, now int64) error {
	if a.ExpiresAtMillis-now < a.Timeouts.OverallMillis {
		return errors.New("updater: runner authority must cover its entire fixed recovery window")
	}
	return nil
}

func (s *Supervisor) boundedRunnerRemaining() (time.Duration, error) {
	b := s.state.Bounded
	authorizedMillis := b.Authorization.ExpiresAtMillis - s.opts.Now().UnixMilli()
	if authorizedMillis <= 0 {
		b.Outcome = "timed_out"
		recorded := false
		for _, entry := range b.Audit {
			if entry.RequestID == b.Authorization.RequestID && entry.Action == "authority_expired" {
				recorded = true
				break
			}
		}
		if !recorded {
			b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "authority_expired", Phase: b.Phase, Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "timed_out", PreviousWindow: boundedWindowSnapshot(b)})
		}
		return 0, errors.New("updater: bounded runner authority expired; explicit external recovery required")
	}
	remaining, err := s.boundedRemaining()
	if err != nil {
		return 0, err
	}
	return time.Duration(min(int64(remaining/time.Millisecond), authorizedMillis)) * time.Millisecond, nil
}

func (s *Supervisor) checkBoundedRunner(ctx context.Context) error {
	op := s.state.Operation
	if s.opts.Authority == nil {
		return errors.New("updater: fixed runner health authority is unavailable")
	}
	health, err := s.opts.Authority.Health(ctx, op.Binding)
	if err != nil {
		return err
	}
	if health.Binding != op.Binding || !health.ServerConfirmed || health.ServerRevision <= 0 || !health.AdmissionFenced || health.NewWorkAccepted || !health.Healthy || health.BoundedRecoveryAuthorizationID != boundedRunnerRecoveryID(s.state.Bounded) || health.JournalVersion < 17 {
		return errors.New("updater: bounded runner process health and recovery fence are not confirmed")
	}
	actual, err := s.opts.Process.Inspect(ctx, op.CandidateProcess.PID)
	if err != nil {
		return err
	}
	if actual != op.CandidateProcess {
		return ErrUnknown
	}
	if health.HeartbeatSequence <= op.LastHealthSequence {
		return nil
	}
	op.LastHealthSequence = health.HeartbeatSequence
	op.HealthCount++
	if op.HealthCount >= 2 {
		s.state.Current = op.Candidate
		s.state.Process = op.CandidateProcess
		op.Irreversible = true
		op.Phase = "committed"
		s.state.Bounded.Outcome = "installed_recovery_pending"
	}
	return s.save()
}

func boundedRunnerRecoveryID(b *protocol.BoundedMaintenanceState) string {
	if b.RecoveryAuthorizationID != "" {
		return b.RecoveryAuthorizationID
	}
	return b.Authorization.RequestID
}

func (s *Supervisor) verifyBoundedRunnerSourceGone(ctx context.Context, source Identity) error {
	found, err := s.opts.Process.Find(ctx, source.LaunchID, source.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 0 {
		return errors.New("updater: predecessor launch still has a possible journal writer")
	}
	return nil
}

func boundedRunnerMayKill(b *protocol.BoundedMaintenanceState) bool {
	authorized := false
	for _, event := range b.Audit {
		authority, known := b.Authorizations[event.RequestID]
		if !known || authority.PlanSHA256 != b.Authorization.PlanSHA256 || authority.Source != b.Authorization.Source || authority.Target != b.Authorization.Target {
			continue
		}
		if event.Action == "kill_predecessor" {
			return false
		}
		if event.Action == "force" || event.Action == "takeover" || event.Action == "force_on_drain_timeout" {
			authorized = true
		}
	}
	return authorized
}

func boundedRunnerPhaseBudget(phase string, p protocol.MaintenanceTimeoutPolicy) int64 {
	switch phase {
	case "handoff":
		return p.HandoffMillis
	case "observation_drain":
		return p.DrainMillis
	case "stop":
		return p.StopMillis
	case "kill":
		return p.KillMillis
	case "backup":
		return p.BackupMillis
	case "start_health":
		return p.StartHealthMillis
	case "complete":
		return p.CompleteMillis
	default:
		return p.FenceMillis
	}
}

// backupBoundedRunnerData reconciles completed or partial original backup bytes
// in place. Existing bytes must match the still-stopped source; they are never
// overwritten by a new copy after a lost backup reply.
func backupBoundedRunnerData(cfg Config, op *Operation) error {
	if err := os.MkdirAll(op.BackupDir, 0700); err != nil {
		return err
	}
	paths := dataPaths(cfg)
	files := make([]BackupFile, 0, len(paths))
	for i, path := range paths {
		file := BackupFile{Path: path, Name: fmt.Sprintf("data-%03d", i)}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			files = append(files, file)
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("updater: bounded runner backup source is not a regular file")
		}
		file.Existed = true
		file.Mode = uint32(info.Mode().Perm())
		file.SHA256, err = hashFile(path)
		if err != nil {
			return err
		}
		destination := filepath.Join(op.BackupDir, file.Name)
		if prior, err := os.Lstat(destination); err == nil {
			if !prior.Mode().IsRegular() || prior.Mode()&os.ModeSymlink != 0 {
				return errors.New("updater: original bounded backup type changed")
			}
			if hash, err := hashFile(destination); err != nil || hash != file.SHA256 {
				return errors.New("updater: original bounded backup bytes do not match the retained source")
			}
		} else if os.IsNotExist(err) {
			if err := copyAtomic(path, destination, 0600); err != nil {
				return err
			}
		} else {
			return err
		}
		files = append(files, file)
	}
	manifest := filepath.Join(op.BackupDir, "files.json")
	if raw, err := os.ReadFile(manifest); err == nil {
		var prior []BackupFile
		if err := json.Unmarshal(raw, &prior); err != nil {
			return err
		}
		if digest(prior) != digest(files) {
			return errors.New("updater: completed bounded backup inventory changed")
		}
	} else if os.IsNotExist(err) {
		if err := writeJSON(manifest, files); err != nil {
			return err
		}
	} else {
		return err
	}
	op.Backups = files
	return nil
}

// A committed process remains under its existing same-version supervisor.
// Its retained force window authorizes no new native business execution.
func (s *Supervisor) boundedRunnerCommittedScope() bool {
	b, proof := s.state.Bounded, s.state.CommittedProof
	return s.cfg.Role == "runner" && b != nil && b.Authorization.Role == "runner" && b.SourceExit != nil && b.SourceExit.Process == b.Authorization.Source &&
		proof != nil && proof.Binding == b.Authorization.Target && proof.Version == s.state.Current &&
		proof.Version.Artifact.SHA256 == b.Authorization.Target.ArtifactSHA256
}

func (s *Supervisor) boundedRunnerSupervisedRestart(op *Operation) bool {
	return op != nil && op.Kind == "restart" && s.boundedRunnerCommittedScope() &&
		op.Binding == s.state.CommittedProof.Binding && op.Previous == s.state.Current && op.Candidate == s.state.Current
}

// Cancellation preserves only supervision that already existed before the
// replacement was authorized. It never adopts a manual or uncommitted source.
func (s *Supervisor) boundedRunnerCancelledOwnedScope() bool {
	b, op := s.state.Bounded, s.state.Operation
	return s.cfg.Role == "runner" && b != nil && b.Authorization.Role == "runner" && b.Outcome == "cancelled" && b.SourceExit == nil &&
		op != nil && op.Kind == "upgrade" && op.Phase == "cancelled" && op.BoundedSourceOwned &&
		validIdentity(s.state.Process) && s.state.Process == b.Authorization.Source && op.PreviousProcess == s.state.Process &&
		op.Previous == s.state.Current && op.Previous.Artifact.SHA256 == s.state.Process.SHA256
}

func (s *Supervisor) boundedRunnerCancelledRestart(op *Operation) bool {
	return s.cfg.Role == "runner" && op != nil && op.Kind == "restart" && op.BoundedSourceOwned &&
		op.Previous == op.Candidate && op.Previous == s.state.Current
}

func (s *Supervisor) boundedRunnerRestartSourceGone(ctx context.Context, source Identity) error {
	actual, err := s.opts.Process.Inspect(ctx, source.PID)
	if err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	if err == nil && actual.StartTicks == source.StartTicks {
		return ErrUnknown
	}
	return s.verifyBoundedRunnerSourceGone(ctx, source)
}
