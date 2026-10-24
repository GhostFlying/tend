package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) loadControl(ctx context.Context) error {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	return r.loadControlLocked(ctx)
}
func (r *Runner) loadControlLocked(ctx context.Context) error {
	if r.controlLoaded {
		return nil
	}
	value, err := r.cfg.Journal.Control(ctx)
	if err != nil {
		return err
	}
	if value.Mode != "" && value.Mode != protocol.RunnerNormal && value.Mode != protocol.RunnerMaintenance {
		return errors.New("journal contains unknown runner control mode")
	}
	if value.Mode == "" {
		value.Mode = protocol.RunnerNormal
	}
	generation, err := r.cfg.Journal.NextProcessGeneration(ctx)
	if err != nil {
		return err
	}
	r.generation = generation
	r.control = value
	r.controlLoaded = true
	r.instanceID = r.cfg.InstanceID
	if r.instanceID == "" {
		r.instanceID = r.cfg.IDs.New(protocol.Prefix("rni"))
	}
	return nil
}

// runtimeObservation never marks unresolved journal work ready. Sequence belongs
// to this process, while the control revision and uncertain operations persist.
func (r *Runner) runtimeObservation(ctx context.Context) (*protocol.RunnerRuntime, error) {
	if err := r.loadControl(ctx); err != nil {
		return nil, err
	}
	r.maybeCancelStaged(ctx)
	receipt := r.updaterProcessReceipt(ctx)
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	if err := r.loadControlLocked(ctx); err != nil {
		return nil, err
	}
	runs, err := r.cfg.Journal.Runs(ctx)
	if err != nil {
		return nil, err
	}
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return nil, err
	}
	releases, err := r.cfg.Journal.WorkspaceReleases(ctx)
	if err != nil {
		return nil, err
	}
	blockers := []string{}
	claims := int64(0)
	for _, p := range requests {
		if p.Envelope.Operation == protocol.OpRunnerClaim {
			claims++
		}
	}
	for _, run := range runs {
		reason := "run:" + run.Assignment.RunID
		switch {
		case run.Held:
			reason = "held_run:" + run.Assignment.RunID
		case run.Fenced:
			reason = "fenced_run:" + run.Assignment.RunID
		case !protocol.RunnerProtocolSupported(run.Assignment.Protocol):
			reason = "unsupported_run_protocol:" + run.Assignment.RunID
		}
		blockers = append(blockers, reason)
	}
	if claims > 0 {
		blockers = append(blockers, "unconfirmed_claim")
	}
	if len(requests) > 0 {
		blockers = append(blockers, "pending_operation")
	}
	if len(releases) > 0 {
		blockers = append(blockers, "pending_workspace_release")
	}
	r.mu.Lock()
	if len(r.active) > 0 && len(runs) == 0 {
		blockers = append(blockers, "local_executor")
	}
	reconciled, busy := r.resourceReconciled, r.resourceBusy
	blockers = append(blockers, r.resourceBlockers...)
	authorizationID := r.grantID
	authorizationDeadline := r.grantServerDeadline
	phase, upgradeError, downloaded := r.upgradePhase, r.upgradeError, r.downloadedBytes
	r.mu.Unlock()
	mode := r.control.Mode
	if r.bootstrapMaintenance {
		mode = protocol.RunnerMaintenance
	}
	if mode == protocol.RunnerMaintenance {
		if !reconciled {
			blockers = append(blockers, "workspace_reconciliation_pending")
		}
		if busy {
			blockers = append(blockers, "workspace_operation_active")
		}
		if len(blockers) == 0 {
			mode = protocol.RunnerReady
		}
	}
	upgradeID := r.control.UpgradeID
	if r.bootstrapMaintenance {
		upgradeID = r.cfg.UpgradeID
	}
	capabilities := []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance, protocol.CapabilityTaskInputs, protocol.CapabilityWorkerQuestions}
	for _, provider := range r.cfg.DeliveryProviders {
		if r.cfg.DeliveryObserver != nil {
			switch provider {
			case "github":
				capabilities = append(capabilities, protocol.CapabilityDeliveryGitHub)
			case "codebase":
				capabilities = append(capabilities, protocol.CapabilityDeliveryCodebase)
			}
		}
	}
	if r.cfg.UpdaterSocket != "" && r.cfg.UpdaterControlSocket != "" && r.cfg.UpdaterBundleDir != "" {
		capabilities = append(capabilities, "runner-updater-v1")
	}
	confirmedCancelled := ""
	if r.cancelledUpdaterID == r.control.CancelledUpgradeID {
		confirmedCancelled = r.cancelledUpdaterID
	}
	r.sequence++
	return &protocol.RunnerRuntime{JournalVersion: 9, InstanceID: r.instanceID, Generation: r.generation, Sequence: r.sequence, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Build: r.cfg.Build, Capabilities: capabilities, Mode: mode, UpgradePhase: phase, UpgradeError: upgradeError, DownloadedBytes: downloaded, UpgradeID: upgradeID, AppliedControlRevision: r.control.Revision, PendingClaims: claims, PendingOperations: int64(len(requests) + len(releases)), Blockers: blockers, AuthorizationID: authorizationID, AuthorizationDeadlineMillis: authorizationDeadline, UpdaterReceipt: receipt, ConfirmedCancelledUpgradeID: confirmedCancelled}, nil
}

func sameGrant(a, b *protocol.MaintenanceAuthorization) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, br := slices.Clone(a.RunIDs), slices.Clone(b.RunIDs)
	slices.Sort(ar)
	slices.Sort(br)
	return a.ID == b.ID && a.DeadlineMillis == b.DeadlineMillis && slices.Equal(ar, br)
}

// applyControl persists before acknowledging it. Local validity uses send time;
// the acknowledged server deadline is a conservative clock-independent bound.
func (r *Runner) applyControl(ctx context.Context, next *protocol.RunnerControl, sent time.Time, hello bool) error {
	if next == nil {
		return nil
	} // Legacy servers cannot authorize maintenance.
	defer r.maybeCancelStaged(ctx)
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	if err := r.loadControlLocked(ctx); err != nil {
		return err
	}
	if next.Revision < r.control.Revision {
		return nil
	}
	if next.Mode != protocol.RunnerNormal && next.Mode != protocol.RunnerMaintenance || next.Revision < 0 {
		return errors.New("invalid runner control")
	}
	if next.Revision == r.control.Revision {
		oldCopy, newCopy := r.control, *next
		oldCopy.Authorization = nil
		newCopy.Authorization = nil
		a, _ := json.Marshal(oldCopy)
		b, _ := json.Marshal(newCopy)
		if string(a) != string(b) || (next.Authorization != nil && r.control.Authorization != nil && !sameGrant(r.control.Authorization, next.Authorization)) {
			return errors.New("runner control revision reused for different command")
		}
	}
	grant := next.Authorization
	if grant != nil {
		if grant.ID == "" || grant.DeadlineMillis <= 0 || grant.RemainingMillis < 0 || grant.RemainingMillis > 900000 {
			return errors.New("invalid fixed maintenance authorization")
		}
		if r.control.Authorization != nil && r.control.Authorization.ID == grant.ID && !sameGrant(r.control.Authorization, grant) {
			return errors.New("maintenance authorization identity changed")
		}
	}
	// An ordinary control/lease renewal cannot retract a still-issued grant.
	stored := *next
	if grant == nil && r.control.Authorization != nil {
		stored.Authorization = r.control.Authorization
	}
	if err := r.cfg.Journal.SaveControl(ctx, stored); err != nil {
		return fmt.Errorf("persist runner control: %w", err)
	}
	modeChanged := r.control.Mode != stored.Mode || r.control.UpgradeID != stored.UpgradeID
	attemptChanged := modeChanged || next.Mode == protocol.RunnerMaintenance && next.Revision > r.control.Revision && next.Upgrade == nil
	cancelledBootstrap := next.Mode == protocol.RunnerNormal && next.UpgradeID == "" && r.control.UpgradeID == r.cfg.UpgradeID && r.cfg.UpgradeID != "" && next.Revision > r.control.Revision
	r.control = stored
	if (next.UpgradeID == r.cfg.UpgradeID && next.Revision > 0 || cancelledBootstrap) && (hello || r.helloConfirmed) {
		r.bootstrapMaintenance = false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if modeChanged {
		r.resourceReconciled = false
	}
	if attemptChanged {
		r.upgradeError = ""
		r.upgradePhase = ""
		r.downloadedBytes = 0
	}
	if hello {
		r.helloConfirmed = true
	}
	if grant != nil && r.helloConfirmed && r.grantID != grant.ID {
		// Round both the server-time haircut and RTT upward. Neither calculation
		// depends on agreement between the runner's and server's wall clocks.
		haircut := (grant.RemainingMillis + 3) / 4
		elapsed := max(time.Duration(0), r.cfg.Now().Sub(sent))
		elapsedMillis := int64(elapsed / time.Millisecond)
		if elapsed%time.Millisecond != 0 {
			elapsedMillis++
		}
		r.grantServerDeadline = grant.DeadlineMillis - haircut - elapsedMillis
		deadline := sent.Add(time.Duration(grant.RemainingMillis-haircut) * time.Millisecond)
		for _, id := range grant.RunIDs {
			r.grantDeadlines[id] = maxTime(r.grantDeadlines[id], deadline)
			if a := r.active[id]; a != nil && a.deadline.Before(r.grantDeadlines[id]) {
				a.deadline = r.grantDeadlines[id]
			}
		}
		r.grantID = grant.ID
	}

	return nil
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func (r *Runner) authorizedDeadline(runID string, normal time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if grant := r.grantDeadlines[runID]; grant.After(normal) {
		return grant
	}
	return normal
}
func (r *Runner) maintenance() bool {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	return r.controlLoaded && (r.control.Mode == protocol.RunnerMaintenance || r.bootstrapMaintenance)
}
