package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// CommittedMaintenance is separate from upgrade and forward-recovery history.
// Neither an old terminal receipt nor an unknown business run is rewritten.
type CommittedMaintenance struct {
	LaunchExit                   *LaunchExitProof                            `json:"launch_exit,omitempty"`
	ReceiptRecoveryAuthorization *protocol.MaintenanceReceiptRecoveryRequest `json:"receipt_recovery_authorization,omitempty"`
	ReceiptRecoveryAuthorized    bool                                        `json:"receipt_recovery_authorized,omitempty"`
	ReceiptRecoveryRequestID     string                                      `json:"receipt_recovery_request_id,omitempty"`
	ReceiptRecoveryFirstSHA256   string                                      `json:"receipt_recovery_first_sha256,omitempty"`
	ReceiptRecoverySecondSHA256  string                                      `json:"receipt_recovery_second_sha256,omitempty"`
	ArchiveAuthorization         *protocol.ArchiveOnlineStatus               `json:"archive_authorization,omitempty"`
	Plan                         protocol.CommittedMaintenancePlan           `json:"plan"`
	OriginalStateSHA256          string                                      `json:"original_state_sha256"`
	ActiveContinuation           *protocol.MaintenanceContinuation           `json:"active_continuation,omitempty"`
	Continuations                map[string]protocol.MaintenanceContinuation `json:"continuations,omitempty"`
	OriginalProof                CommittedProof                              `json:"original_proof"`
	Target                       Version                                     `json:"target"`
	Phase                        string                                      `json:"phase"`
	Process                      Identity                                    `json:"process"`
	AuthorizationBootID          string                                      `json:"authorization_boot_id,omitempty"`
	AuthorizationDeadlineMillis  int64                                       `json:"authorization_deadline_millis,omitempty"`
	Backups                      []BackupFile                                `json:"backups,omitempty"`
	Irreversible                 bool                                        `json:"irreversible"`
	Epoch                        string                                      `json:"epoch,omitempty"`
	FirstSequence                uint64                                      `json:"first_sequence,omitempty"`
	LastSequence                 uint64                                      `json:"last_sequence,omitempty"`
	HealthCount                  int                                         `json:"health_count"`
	Receipt                      *protocol.CommittedMaintenanceReceipt       `json:"receipt,omitempty"`
	LastError                    string                                      `json:"last_error,omitempty"`
}

type MaintenanceAuthority interface {
	MaintenanceHealth(context.Context, protocol.CommittedMaintenancePlan) (protocol.CommittedMaintenanceHealth, error)
}

func maintenanceResources(p protocol.CommittedMaintenancePlan) protocol.ForwardRecoveryPlan {
	// Resource evidence has the same operator/expiry contract. This projection
	// is never registered as a recovery or used to authorize an old operation.
	return protocol.ForwardRecoveryPlan{WindowClaim: p.WindowClaim, OperatorUID: p.OperatorUID, ExpiresAtMillis: p.ExpiresAtMillis, ResourceEvidence: p.ResourceEvidence, ResourceSHA256: p.ResourceSHA256}
}

func (s *Supervisor) committedBundle(p protocol.CommittedMaintenancePlan) (*release.Bundle, release.Artifact, error) {
	b, err := release.Load(p.ReleaseDir)
	if err != nil {
		return nil, release.Artifact{}, err
	}
	a, err := b.Artifact("server", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, a, err
	}
	if err := b.Verify(a); err != nil {
		return nil, a, err
	}
	if p.ParentMaintenanceID != "" {
		parent, err := s.maintenanceParent(p)
		if err != nil {
			return nil, a, err
		}
		if !b.Manifest.Protocol.Contains(parent.Target.Protocol) || !b.Manifest.Storage.ServerSchema.Contains(parent.Target.StorageVersion) {
			return nil, a, errors.New("updater: successor must cover all irreversible parent schema versions")
		}
	}
	protocolVersion, storageVersion := s.state.Current.Protocol, s.state.Current.StorageVersion
	if source := p.SourceOperation; source != nil {
		protocolVersion, storageVersion = source.Protocol, source.StorageVersion
	}
	if b.Manifest.ReleaseID != p.Successor.ReleaseID || b.ManifestSHA256 != p.Successor.ManifestSHA256 || a.SHA256 != p.Successor.ArtifactSHA256 || !b.Manifest.Protocol.Contains(protocolVersion) || !b.Manifest.Storage.ServerSchema.Contains(storageVersion) {
		return nil, a, errors.New("updater: maintenance successor is incompatible with committed data")
	}
	return b, a, nil
}

func (s *Supervisor) validateCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, generation bool) error {
	if s.cfg.Role != "server" || !release.ValidID(p.MaintenanceID) || !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || !protocol.ValidID(protocol.PrefixRequest, p.LinkRequestID) || p.RequestID == p.LinkRequestID || p.OperatorUID != os.Getuid() || p.WindowClaim == "" || p.PreparationRevision < 1 || p.ExpiresAtMillis <= s.opts.Now().UnixMilli() || p.ExpiresAtMillis > s.opts.Now().Add(30*time.Minute).UnixMilli() || p.ConfigSHA256 != digest(s.cfg) {
		return errors.New("updater: incomplete committed maintenance authorization")
	}
	if err := validateMaintenanceRequests(p); err != nil {
		return err
	}
	if s.cfg.AuthoritySocket == "" || s.cfg.CoordinatorSocket == "" {
		return errors.New("updater: fixed maintenance authority required")
	}
	if p.SourceOperation != nil {
		if err := s.validateOperationSource(ctx, p); err != nil {
			return err
		}
	} else if s.state.CommittedProof == nil || s.state.Current != s.state.CommittedProof.Version || s.state.Process != s.state.CommittedProof.Process {
		return errors.New("updater: exact committed service proof required")
	}
	if p.SourceOperation == nil && !done(s.state.Operation) || s.state.Recovery != nil && s.state.Recovery.Phase != "ledger_linked" {
		return errors.New("updater: another legacy operation owns this service")
	}
	if p.SourceOperation != nil {
		if !maintenanceTerminal(s.state.Maintenance) {
			return errors.New("updater: another maintenance owns the unfinished source")
		}
	} else if p.ParentMaintenanceID == "" {
		if !maintenanceTerminal(s.state.Maintenance) || p.ParentPlan != nil || p.ParentPlanSHA256 != "" || p.ParentArchiveSHA256 != "" || p.ParentGeneration != 0 || p.ParentReceipt != nil || p.ParentLaunchExit != nil || p.FailedProcessExited || s.state.CommittedProof.Binding != p.Previous || s.state.Process != p.PreviousProcess {
			return errors.New("updater: exact committed service source required")
		}
	} else {
		parent, err := s.maintenanceParent(p)
		if err != nil {
			return err
		}
		if parent.Plan.MaintenanceID == p.MaintenanceID || parent.Plan.Successor != p.Previous || parent.Process != p.PreviousProcess || p.ParentGeneration != p.ExpectedGeneration || p.PreparationRevision != parent.Plan.PreparationRevision || p.PreparationRequestID != parent.Plan.PreparationRequestID || p.PreparationCancelRequestID != parent.Plan.PreparationCancelRequestID || digest(p.HostControls) != digest(parent.Plan.HostControls) {
			return errors.New("updater: failed maintenance source or admission responsibility changed")
		}
	}
	exitedLaunch := (p.ParentMaintenanceID != "" || p.SourceOperation != nil) && p.PreviousProcess == (Identity{})
	// Ordinary maintenance already matches the historical committed binding and
	// the current verified process separately. A same-version restart preserves
	// that binding; failed-parent recovery must retain its original launch.
	parentLaunchChanged := p.ParentMaintenanceID != "" && p.Previous.ProcessInstanceID != p.PreviousProcess.LaunchID
	if !exitedLaunch && (!validIdentity(p.PreviousProcess) || p.Previous.ArtifactSHA256 != p.PreviousProcess.SHA256 || parentLaunchChanged) {
		return errors.New("updater: exact maintenance process binding required")
	}
	if generation && (p.ExpectedGeneration != s.state.Generation || p.StateSHA256 != digest(s.state)) {
		return errors.New("updater: committed state generation changed")
	}
	if p.Successor.UpgradeID != p.MaintenanceID || !validRollbackBinding(p.Successor, s.cfg.InstanceID) || p.Successor.ProcessInstanceID == p.Previous.ProcessInstanceID || p.Successor.ProcessInstanceID == p.PreviousProcess.LaunchID || !independentMaintenanceArtifact(p) {
		return errors.New("updater: independent maintenance successor required")
	}
	if err := verifyResourceEvidence(maintenanceResources(p), s.opts.Now()); err != nil {
		return err
	}
	if _, _, err := s.committedBundle(p); err != nil {
		return err
	}
	if p.SourceOperation != nil {
		return nil
	}
	if exitedLaunch {
		parent, err := s.maintenanceParent(p)
		if err != nil {
			return err
		}
		return s.maintenanceLaunchGone(ctx, parent)
	}
	if p.FailedProcessExited {
		return s.maintenanceSourceGone(ctx, p.PreviousProcess)
	}
	id, err := s.opts.Process.Inspect(ctx, p.PreviousProcess.PID)
	if err != nil {
		return err
	}
	if id != p.PreviousProcess {
		return ErrUnknown
	}
	found, err := s.opts.Process.Find(ctx, id.LaunchID, id.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 1 || found[0] != id {
		return ErrUnknown
	}
	return nil
}

// AdoptCommittedMaintenance is called only after the independent installer chain
// has verified this updater's bytes, original child, manager and lock ownership.
func (s *Supervisor) AdoptCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if m := s.state.Maintenance; m != nil && m.Plan.MaintenanceID == p.MaintenanceID {
		if digest(m.Plan) != digest(p) {
			return errors.New("updater: maintenance payload changed")
		}
		return nil
	}
	if _, ok := s.state.MaintenanceHistory[p.MaintenanceID]; ok {
		return errors.New("updater: historical maintenance identity cannot be reused")
	}
	if err := s.validateCommittedMaintenance(ctx, p, false); err != nil {
		return err
	}
	b, a, err := s.committedBundle(p)
	if err != nil {
		return err
	}
	target, err := installVersion(s.cfg.StateDir, b, a)
	if err != nil {
		return err
	}
	if s.state.Maintenance != nil {
		if s.state.MaintenanceHistory == nil {
			s.state.MaintenanceHistory = map[string]CommittedMaintenance{}
		}
		s.state.MaintenanceHistory[s.state.Maintenance.Plan.MaintenanceID] = *s.state.Maintenance
	}
	if b := s.state.Bounded; b != nil {
		if s.state.BoundedHistory == nil {
			s.state.BoundedHistory = map[string]protocol.BoundedMaintenanceState{}
		}
		s.state.BoundedHistory[b.Authorization.RequestID] = *b
		s.state.Bounded = nil
	}
	originalProof := CommittedProof{}
	if s.state.CommittedProof != nil {
		originalProof = *s.state.CommittedProof
	}
	if err := s.archiveOperationSource(p); err != nil {
		return err
	}
	s.state.Maintenance = &CommittedMaintenance{Plan: p, OriginalStateSHA256: p.StateSHA256, OriginalProof: originalProof, Target: target, Phase: "updater_adopted"}
	if p.ParentMaintenanceID != "" && p.ReceiptRecoveryStop != nil {
		parent := s.state.MaintenanceHistory[p.ParentMaintenanceID]
		s.state.Maintenance.ReceiptRecoveryAuthorized = parent.ReceiptRecoveryAuthorized
		s.state.Maintenance.ReceiptRecoveryRequestID = parent.ReceiptRecoveryRequestID
		s.state.Maintenance.ReceiptRecoveryAuthorization = parent.ReceiptRecoveryAuthorization
		s.state.Maintenance.ReceiptRecoveryFirstSHA256 = parent.ReceiptRecoveryFirstSHA256
		s.state.Maintenance.ReceiptRecoverySecondSHA256 = parent.ReceiptRecoverySecondSHA256
	}
	if s.state.UpdaterEpoch == "" {
		epoch, err := s.opts.NewID()
		if err != nil {
			return err
		}
		boot, _, err := s.opts.BootClock()
		if err != nil {
			return err
		}
		s.state.UpdaterEpoch = epoch
		s.state.UpdaterBootID = boot
	}
	if s.state.Format < 3 {
		s.state.Format = 3
	}
	return s.save()
}

func (s *Supervisor) AuthorizeCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) {
		return errors.New("updater: unknown committed maintenance request")
	}
	if m.Phase != "updater_adopted" {
		return nil
	}
	if generation != s.state.Generation {
		return errors.New("updater: stale maintenance generation")
	}
	if p.ReceiptRecoveryStop != nil && p.ParentMaintenanceID == "" {
		return errors.New("updater: explicit receipt recovery stop authorization required")
	}
	if err := verifyResourceEvidence(maintenanceResources(p), s.opts.Now()); err != nil {
		return err
	}
	if p.ParentMaintenanceID == "" {
		a, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
		if !ok {
			return errors.New("updater: maintenance archive authority unavailable")
		}
		v, err := a.MaintenanceArchiveStatus(ctx)
		if err != nil {
			return err
		}
		if err := maintenanceDrain(p, v); err != nil {
			return err
		}
		m.ArchiveAuthorization = &v
	} else {
		parent, err := s.maintenanceParent(p)
		if err != nil {
			return err
		}
		if err := s.validateFailedAdmission(ctx, p, parent); err != nil {
			return err
		}
		raw, _ := json.Marshal(parent.ArchiveAuthorization)
		var copy protocol.ArchiveOnlineStatus
		_ = json.Unmarshal(raw, &copy)
		m.ArchiveAuthorization = &copy
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	remaining := p.ExpiresAtMillis - s.opts.Now().UnixMilli()
	if remaining <= 0 || remaining > (30*time.Minute).Milliseconds() {
		return errors.New("updater: maintenance window expired")
	}
	m.AuthorizationBootID = boot
	m.AuthorizationDeadlineMillis = mono + remaining
	m.Phase = "authorized"
	return s.save()
}

func (s *Supervisor) maintenanceAuthorityValid(m *CommittedMaintenance) error {
	if b := s.state.Bounded; b != nil && b.Authorization.Role == "server" {
		_, err := s.boundedRemaining()
		return err
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	if boot != m.AuthorizationBootID || mono >= m.AuthorizationDeadlineMillis || s.opts.Now().UnixMilli() >= maintenanceActiveResources(m).ExpiresAtMillis {
		return errors.New("updater: maintenance authority expired")
	}
	return nil
}

func (s *Supervisor) maintenanceOldGone(ctx context.Context, m *CommittedMaintenance) error {
	if source := m.Plan.SourceOperation; source != nil && m.Plan.PreviousProcess == (Identity{}) {
		op, exists := s.state.OperationHistory[source.OperationID]
		if !exists || digest(op) != source.OperationSHA256 {
			return errors.New("updater: original launch history unavailable")
		}
		return s.operationSourceExited(ctx, m.Plan, &op)
	}
	if m.Plan.ParentMaintenanceID != "" && m.Plan.PreviousProcess == (Identity{}) {
		parent, err := s.maintenanceParent(m.Plan)
		if err != nil {
			return err
		}
		return s.maintenanceLaunchGone(ctx, parent)
	}
	id, err := s.opts.Process.Inspect(ctx, m.Plan.PreviousProcess.PID)
	if err == nil && id.StartTicks == m.Plan.PreviousProcess.StartTicks {
		return ErrUnknown
	}
	if err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	found, err := s.opts.Process.Find(ctx, m.Plan.PreviousProcess.LaunchID, m.Plan.PreviousProcess.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 0 {
		return ErrUnknown
	}
	return nil
}

func (s *Supervisor) maintenanceStarted(m *CommittedMaintenance, id Identity) error {
	if !validIdentity(id) || id.LaunchID != m.Plan.Successor.ProcessInstanceID || id.SHA256 != m.Target.Artifact.SHA256 {
		return ErrUnknown
	}
	m.Process = id
	m.Phase = "checking_successor"
	return s.save()
}

func (s *Supervisor) maintenanceStep(ctx context.Context) error {
	m := s.state.Maintenance
	switch m.Phase {
	case "updater_adopted", "successor_committed", "ledger_linked", "cancelled", "cancelling":
		return nil
	case "authorized":
		if err := s.maintenanceAuthorityValid(m); err != nil {
			return err
		}
		if err := verifyResourceEvidence(maintenanceActiveResources(m), s.opts.Now()); s.state.Bounded == nil && err != nil {
			return err
		}
		id := m.Plan.PreviousProcess
		if m.Plan.FailedProcessExited {
			if err := s.maintenanceOldGone(ctx, m); err != nil {
				return err
			}
		} else {
			actual, err := s.opts.Process.Inspect(ctx, id.PID)
			if err != nil {
				return err
			}
			if actual != id {
				return ErrUnknown
			}
		}
		if s.state.Bounded != nil && s.state.Bounded.Authorization.Role == "server" {
			// Explicit force keeps the raw observation; it does not assert drain.
		} else if m.Plan.ParentMaintenanceID == "" {
			a, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
			if !ok {
				return errors.New("updater: maintenance archive authority unavailable")
			}
			v, err := a.MaintenanceArchiveStatus(ctx)
			if err != nil {
				return err
			}
			var admissionErr error
			if m.ReceiptRecoveryAuthorized {
				admissionErr = ValidateReceiptRecoveryArchive(m.Plan, v)
			} else {
				admissionErr = maintenanceDrain(m.Plan, v)
			}
			if admissionErr != nil {
				return admissionErr
			}
			m.ArchiveAuthorization = &v
		} else {
			parent, err := s.maintenanceParent(m.Plan)
			if err != nil {
				return err
			}
			if err := s.validateFailedAdmission(ctx, m.Plan, parent); err != nil {
				return err
			}
		}
		m.Phase = "stopping_old"
		if err := s.save(); err != nil {
			return err
		}
		if !m.Plan.FailedProcessExited {
			var stopErr error
			if m.ReceiptRecoveryAuthorized {
				crash, ok := s.opts.Process.(receiptRecoveryProcess)
				if !ok {
					return errors.New("updater: exact crash recovery process support unavailable")
				}
				stopErr = crash.Crash(ctx, id, time.Duration(s.cfg.StopTimeoutSeconds)*time.Second)
			} else {
				stopTimeout := time.Duration(s.cfg.StopTimeoutSeconds) * time.Second
				if b := s.state.Bounded; b != nil {
					stopTimeout = time.Duration(b.Authorization.Timeouts.StopMillis) * time.Millisecond
				}
				stopCtx, cancel := context.WithTimeout(ctx, stopTimeout)
				stopErr = s.opts.Process.Stop(stopCtx, id, stopTimeout)
				cancel()
			}
			if stopErr != nil {
				return stopErr
			}
		}
		return s.maintenanceRecordOldExit(ctx, m)
	case "stopping_old":
		return s.maintenanceRecordOldExit(ctx, m)
	case "old_exited":
		if err := s.maintenanceAuthorityValid(m); err != nil {
			return err
		}
		if err := s.maintenanceOldGone(ctx, m); err != nil {
			return err
		}
		op := &Operation{BackupDir: filepath.Join(s.cfg.StateDir, "maintenance-backups", m.Plan.MaintenanceID), StartedAt: s.opts.Now(), Previous: s.state.Current}
		if err := backupDataContext(ctx, s.cfg, op); err != nil {
			return err
		}
		m.Backups = op.Backups
		m.Phase = "backed_up"
		return s.save()
	case "backed_up":
		if err := s.maintenanceAuthorityValid(m); err != nil {
			return err
		}
		if err := s.maintenanceOldGone(ctx, m); err != nil {
			return err
		}
		if sum, err := hashFile(m.Target.Binary); err != nil || sum != m.Target.Artifact.SHA256 {
			return errors.New("updater: maintenance candidate bytes changed")
		}
		m.Phase = "starting_successor"
		m.Irreversible = true
		if err := s.save(); err != nil {
			return err
		}
		extra := map[string]string{"TEND_UPDATER_INSTANCE_ID": s.cfg.InstanceID, "TEND_UPDATER_PROCESS_INSTANCE_ID": m.Plan.Successor.ProcessInstanceID, "TEND_UPDATER_LAUNCH_ID": m.Plan.Successor.ProcessInstanceID, "TEND_RELEASE_SHA256": m.Target.Artifact.SHA256, "TEND_UPDATER_RELEASE_ID": m.Target.ReleaseID, "TEND_COMMITTED_MAINTENANCE_ID": m.Plan.MaintenanceID, "TEND_COMMITTED_MAINTENANCE_SOCKET": SocketPath(s.cfg)}
		if b := s.state.Bounded; b != nil {
			extra["TEND_BOUNDED_RECOVERY_REQUEST_ID"] = boundedRecoveryID(b)
		}
		start := s.opts.Process.Start
		if launcher, ok := s.opts.Process.(maintenanceLauncher); ok {
			start = launcher.StartMaintenance
		}
		id, err := start(ctx, m.Target.Binary, s.cfg, extra)
		if err != nil {
			var exited *launchExitedError
			if errors.As(err, &exited) {
				m.LaunchExit = &exited.proof
				m.Phase = "launch_exited"
				if proofErr := ValidateMaintenanceLaunchExit(*m); proofErr != nil {
					return proofErr
				}
				if saveErr := s.save(); saveErr != nil {
					return saveErr
				}
			}
			if errors.Is(err, ErrNotStarted) {
				m.Phase = "launch_not_started"
				m.Irreversible = false
				if saveErr := s.save(); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
		return s.maintenanceStarted(m, id)
	case "launch_exited":
		return errors.New("updater: owned candidate exited; independent failed-parent forward required")
	case "launch_not_started":
		return errors.New("updater: successor was definitely not launched; fresh continuation required")
	case "starting_successor":
		found, err := s.opts.Process.Find(ctx, m.Plan.Successor.ProcessInstanceID, m.Target.Artifact.SHA256)
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return ErrUnknown
		}
		return s.maintenanceStarted(m, found[0])
	case "checking_successor":
		return s.maintenanceCheck(ctx, m)
	}
	return errors.New("updater: invalid committed maintenance phase")
}

func (s *Supervisor) maintenanceRecordOldExit(ctx context.Context, m *CommittedMaintenance) error {
	if err := s.maintenanceOldGone(ctx, m); err != nil {
		return err
	}
	m.Phase = "old_exited"
	if b := s.state.Bounded; b != nil {
		b.SourceExit = &protocol.BoundedProcessExit{Process: m.Plan.PreviousProcess, ConfirmedAtMillis: s.opts.Now().UnixMilli(), Method: "exact_process_exit_observed"}
	}
	return s.save()
}

func (s *Supervisor) maintenanceCheck(ctx context.Context, m *CommittedMaintenance) error {
	a, ok := s.opts.Authority.(MaintenanceAuthority)
	if !ok {
		return errors.New("updater: fixed maintenance health authority unavailable")
	}
	h, err := a.MaintenanceHealth(ctx, m.Plan)
	id, inspectErr := s.opts.Process.Inspect(ctx, m.Process.PID)
	good := err == nil && inspectErr == nil && id == m.Process && h.MaintenanceID == m.Plan.MaintenanceID && h.Binding == m.Plan.Successor && h.Process == m.Process && h.Ready && h.Registered && h.Fenced && len(h.Failures) == 0 && h.Epoch != "" && h.Protocol == m.Target.Protocol && h.Schema == m.Target.StorageVersion && h.Sequence > 0
	if !good || m.Epoch != "" && (h.Epoch != m.Epoch || h.Sequence <= m.LastSequence) {
		if m.HealthCount == 0 && m.FirstSequence == 0 && m.LastSequence == 0 && m.Epoch == "" {
			return errors.New("updater: maintenance health binding/sequence not confirmed")
		}
		m.HealthCount = 0
		m.FirstSequence = 0
		m.LastSequence = 0
		m.Epoch = ""
		if e := s.save(); e != nil {
			return e
		}
		return errors.New("updater: maintenance health binding/sequence not confirmed")
	}
	m.Epoch = h.Epoch
	m.LastSequence = h.Sequence
	m.HealthCount++
	if m.HealthCount == 1 {
		m.FirstSequence = h.Sequence
		return s.save()
	}
	if s.state.CommittedProof != nil {
		s.state.ProofHistory = append(s.state.ProofHistory, *s.state.CommittedProof)
	}
	s.state.Current = m.Target
	s.state.Process = m.Process
	s.state.CommittedProof = &CommittedProof{Binding: m.Plan.Successor, Version: m.Target, Process: m.Process}
	m.Phase = "successor_committed"
	m.Receipt = &protocol.CommittedMaintenanceReceipt{MaintenanceID: m.Plan.MaintenanceID, RequestID: m.Plan.RequestID, Previous: m.Plan.Previous, Successor: m.Plan.Successor, Process: m.Process, Generation: s.state.Generation + 1, Epoch: m.Epoch, FirstSequence: m.FirstSequence, SecondSequence: m.LastSequence}
	if b := s.state.Bounded; b != nil {
		return s.boundedEnter("complete", b.Authorization.Timeouts.CompleteMillis)
	}
	return s.save()
}

func (s *Supervisor) LinkCommittedMaintenance(proof protocol.CommittedMaintenanceReceipt, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || m.Receipt == nil || *m.Receipt != proof {
		return errors.New("updater: exact maintenance receipt required")
	}
	if m.Phase == "ledger_linked" {
		return nil
	}
	if generation != s.state.Generation || m.Phase != "successor_committed" {
		return errors.New("updater: stale maintenance receipt generation")
	}
	if s.state.Bounded != nil {
		if _, err := s.boundedRemaining(); err != nil {
			s.state.Bounded.Outcome = "installed_recovery_pending"
			if digest(s.state) != digest(s.durable) {
				if saveErr := s.save(); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
	}
	m.Phase = "ledger_linked"
	return s.save()
}
