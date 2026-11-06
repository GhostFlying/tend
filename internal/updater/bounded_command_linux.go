//go:build linux

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func boundedCommand(ctx context.Context, cfg Config, action, planFile, authorizationFile, requestID string, generation uint64, expectedPhase string) (any, error) {
	normalizeConfig(&cfg)
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return nil, err
	}
	if state.InstanceID != cfg.InstanceID {
		return state, errors.New("updater: bounded state belongs to another instance")
	}
	if action == "status" || action == "reconcile" {
		if requestID == "" {
			return state, nil
		}
		if !protocol.ValidID(protocol.PrefixRequest, requestID) {
			return state, errors.New("updater: invalid bounded request filter")
		}
		if state.Bounded != nil {
			if _, ok := state.Bounded.Authorizations[requestID]; ok {
				return map[string]any{"state": state, "authorization": state.Bounded.Authorizations[requestID]}, nil
			}
		}
		if b, ok := state.BoundedHistory[requestID]; ok {
			return map[string]any{"state": state, "bounded": b}, nil
		}
		for _, b := range state.BoundedHistory {
			if a, ok := b.Authorizations[requestID]; ok {
				return map[string]any{"state": state, "bounded": b, "authorization": a}, nil
			}
		}
		if raw, e := privateFile(boundedInstallerPath(cfg, requestID)); e == nil {
			var j boundedInstallerJournal
			if e := strictJSON(raw, &j); e != nil {
				return state, e
			}
			return map[string]any{"state": state, "installer": j}, nil
		}
		return state, errors.New("updater: bounded request was not found")
	}
	var auth protocol.BoundedMaintenanceAuthorization
	raw, err := privateFile(authorizationFile)
	if err != nil {
		return state, err
	}
	if err := strictJSON(raw, &auth); err != nil {
		return state, err
	}
	if requestID != "" && requestID != auth.RequestID || generation != 0 && generation != auth.ExpectedGeneration || expectedPhase != "" && expectedPhase != auth.ExpectedPhase {
		return state, errors.New("updater: bounded input identity or CAS changed")
	}
	if auth.Role != cfg.Role {
		return state, errors.New("updater: bounded role differs from fixed configuration")
	}
	var req ControlRequest
	var window *protocol.BoundedMaintenanceState
	if cfg.Role == "server" {
		plan, err := ReadCommittedHandoffPlan(planFile)
		if err != nil {
			return state, err
		}
		if action == "plan" {
			data, err := boundedServerDataIdentity(cfg)
			if err != nil {
				return state, err
			}
			auth.DataIdentity = data
			auth.Source, auth.Target, auth.PlanSHA256 = plan.Maintenance.PreviousProcess, plan.Maintenance.Successor, digest(plan.Maintenance)
			auth.ExpectedGeneration = state.Generation
			auth.ExpectedPhase = "handoff"
			if state.Maintenance != nil && digest(state.Maintenance.Plan) == digest(plan.Maintenance) {
				auth.ExpectedPhase = state.Maintenance.Phase
			}
			if auth.Timeouts.OverallMillis == 0 {
				auth.Timeouts = protocol.DefaultMaintenanceTimeoutPolicy()
			}
			return map[string]any{"plan": plan, "authorization": auth, "state": state}, nil
		}
		if auth.Source != plan.Maintenance.PreviousProcess || auth.Target != plan.Maintenance.Successor || auth.PlanSHA256 != digest(plan.Maintenance) {
			return state, errors.New("updater: bounded source or target differs from its frozen handoff plan")
		}
		if action != "cancel" {
			if err := validateBoundedServerData(cfg, auth.DataIdentity); err != nil {
				return state, err
			}
		}
		req.Maintenance = &plan.Maintenance
		if action == "takeover" {
			return takeoverBoundedServer(ctx, cfg, plan, auth)
		}

		if auth.ExpectedPhase == "handoff" {
			if auth.ExpectedGeneration != plan.Maintenance.ExpectedGeneration || auth.PlanSHA256 != digest(plan.Maintenance) {
				return state, errors.New("updater: original bounded handoff authorization changed")
			}
			journal, e := beginBoundedInstaller(cfg, auth, action)
			if e != nil {
				return journal, e
			}
			handoffCtx, cancel, e := boundedInstallerContext(ctx, journal)
			if e != nil {
				return journal, e
			}
			defer cancel()
			if journal.Phase == "planned" {
				journal.Phase = "handoff_started"
				if e := writeJSON(boundedInstallerPath(cfg, auth.RequestID), journal); e != nil {
					return journal, e
				}
				if state.Maintenance == nil || digest(state.Maintenance.Plan) != digest(plan.Maintenance) {
					_, _ = ApplyCommittedHandoff(handoffCtx, cfg, plan)
				}
			}
			adopted, e := awaitBoundedAdoption(handoffCtx, cfg, plan, journal)
			if e != nil {
				return map[string]any{"state": adopted, "installer": journal}, e
			}
			if journal.Phase == "handoff_started" {
				boot, mono, e := BootClock()
				if e != nil || boot != journal.Window.BootID {
					return journal, errors.New("updater: installer boot changed before admission fence")
				}
				now := time.Now().UnixMilli()
				journal.Phase = "fence_started"
				journal.Window.Phase = "fence"
				journal.Window.PhaseEnteredAtMillis = now
				journal.Window.PhaseDeadlineMillis = min(now+auth.Timeouts.FenceMillis, journal.Window.OverallDeadlineMillis)
				journal.Window.PhaseDeadlineMono = min(mono+auth.Timeouts.FenceMillis, journal.Window.OverallDeadlineMono)
				if e := writeJSON(boundedInstallerPath(cfg, auth.RequestID), journal); e != nil {
					return journal, e
				}
			}
			window = &journal.Window
			// Preparation uses the original request IDs. Missing ACKs do not authorize
			// fabricated drain; explicit force may still stop the exact old writer.
			prepareCtx, stop, e := boundedInstallerContext(ctx, journal)
			if e == nil {
				_, _ = Call(prepareCtx, cfg, "maintenance/prepare", ControlRequest{Maintenance: &plan.Maintenance, ExpectedGeneration: adopted.Generation})
				stop()
			}
			if e != nil && action != "force" && !auth.ForceOnDrainTimeout {
				return journal, e
			}
		}

	} else if cfg.Role == "runner" {
		var grant protocol.BoundedRunnerMaintenanceGrant
		raw, err := privateFile(planFile)
		if err != nil {
			return state, err
		}
		if err := strictJSON(raw, &grant); err != nil {
			return state, err
		}
		if action == "plan" {
			return map[string]any{"grant": grant, "authorization": auth, "state": state}, nil
		}
		if action == "takeover" {
			return takeoverBoundedRunner(ctx, cfg, grant, auth)
		}
		if digest(grant.Plan.Authorization) != digest(auth) {
			return state, errors.New("updater: runner authorization differs from the original owner grant")
		}
		req.BoundedRunnerGrant = &grant
	} else {
		return state, errors.New("updater: unsupported bounded component")
	}
	if action == "cancel" && req.Maintenance != nil {
		st, e := Call(ctx, cfg, "bounded/cancel", ControlRequest{Maintenance: req.Maintenance, ExpectedGeneration: auth.ExpectedGeneration, BoundedAuthorization: &auth})
		return st, e
	}
	req.BoundedWindow = window
	req.BoundedAuthorization = &auth
	req.BoundedAction = action
	req.ExpectedGeneration = auth.ExpectedGeneration
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := Call(callCtx, cfg, "bounded/authorize", req)
	if err != nil {
		latest, readErr := readState(filepath.Join(cfg.StateDir, "state.json"))
		if readErr == nil {
			return latest, fmt.Errorf("updater: bounded request response unconfirmed; reconcile original request: %w", err)
		}
		return st, err
	}
	return st, nil
}

// takeoverBoundedServer and takeoverBoundedRunner share the process-external
// mechanism. Neither entry requires the old control socket or mutex to answer.
func takeoverBoundedServer(ctx context.Context, cfg Config, p CommittedHandoffPlan, a protocol.BoundedMaintenanceAuthorization) (any, error) {
	if err := validateBoundedServerData(cfg, a.DataIdentity); err != nil {
		return nil, err
	}
	target := protocol.BoundedUpdaterTarget{Unit: p.Unit, UnitSHA256: p.UnitSHA256, ManagerSHA256: p.ManagerSHA256, UpdaterBinary: p.UpdaterBinary, Candidate: p.Candidate, CandidateSHA256: p.CandidateSHA256, ConfigFile: p.ConfigFile, ConfigFileSHA256: p.ConfigFileSHA256}
	return takeoverBounded(ctx, cfg, target, a, func(st State) error {
		if st.Maintenance == nil || st.Maintenance.Phase != a.ExpectedPhase || digest(st.Maintenance.Plan) != digest(p.Maintenance) || a.PlanSHA256 != digest(p.Maintenance) || a.Source != p.Maintenance.PreviousProcess || a.Target != p.Maintenance.Successor {
			return errors.New("updater: external server source plan changed")
		}
		return nil
	}, func(s *Supervisor) error { return s.AuthorizeBoundedMaintenance(ctx, p.Maintenance, a, "takeover") })
}

func takeoverBoundedRunner(ctx context.Context, cfg Config, g protocol.BoundedRunnerMaintenanceGrant, a protocol.BoundedMaintenanceAuthorization) (any, error) {
	if a.UpdaterTarget == nil || digest(g.Plan.Authorization) != digest(a) {
		return nil, errors.New("updater: runner external takeover requires the pinned local supervisor target")
	}
	return takeoverBounded(ctx, cfg, *a.UpdaterTarget, a, func(st State) error {
		phase := "idle"
		if st.Operation != nil {
			phase = st.Operation.Phase
		}
		if st.Bounded != nil {
			phase = st.Bounded.Phase
		}
		if phase != a.ExpectedPhase {
			return errors.New("updater: external runner phase changed")
		}
		return nil
	}, func(s *Supervisor) error { return s.AuthorizeBoundedRunnerMaintenance(ctx, g, "takeover") })
}

func takeoverBounded(ctx context.Context, cfg Config, target protocol.BoundedUpdaterTarget, a protocol.BoundedMaintenanceAuthorization, validate func(State) error, authorize func(*Supervisor) error) (any, error) {
	if a.UpdaterSource == nil || a.UpdaterLock == nil || a.UpdaterSource.PID < 2 || a.UpdaterSource.StartTicks == 0 || !release.ValidSHA(a.UpdaterSource.SHA256) || a.UpdaterLock.Device == 0 || a.UpdaterLock.Inode == 0 || !protocol.ValidID(protocol.PrefixRequest, a.RequestID) || a.Role != cfg.Role || a.Target.InstanceID != cfg.InstanceID || a.Reason == "" {
		return nil, errors.New("updater: external takeover requires exact component, process and lock authority")
	}
	if err := validateTimeoutPolicy(a.Timeouts); err != nil {
		return nil, err
	}
	installLock, err := committedInstallerLock(cfg)
	if err != nil {
		return nil, err
	}
	defer installLock.Close()
	before, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return nil, err
	}
	if before.Generation != a.ExpectedGeneration {
		return before, errors.New("updater: external takeover generation changed")
	}
	if err := validate(before); err != nil {
		return before, err
	}
	if err := validateBoundedUpdaterTarget(ctx, cfg, target); err != nil {
		return before, err
	}
	device, inode, err := lockIdentity(cfg.StateDir)
	if err != nil {
		return before, err
	}
	if device != a.UpdaterLock.Device || inode != a.UpdaterLock.Inode {
		return before, errors.New("updater: external takeover lock changed")
	}
	journal, err := beginBoundedInstaller(cfg, a, "takeover")
	if err != nil {
		return before, err
	}
	phaseCtx, cancel, err := boundedInstallerContext(ctx, journal)
	if err != nil {
		return journal, err
	}
	defer cancel()
	ctx = phaseCtx
	actual, inspectErr := (LinuxProcess{}).Inspect(ctx, a.UpdaterSource.PID)
	if inspectErr == nil {
		if actual != *a.UpdaterSource {
			return before, errors.New("updater: external takeover process identity changed")
		}
		if err := verifyUpdaterCommand(actual.PID, HandoffPlan{UpdaterBinary: target.UpdaterBinary, ConfigFile: target.ConfigFile}); err != nil {
			return before, err
		}
		owns, e := processOwnsFlock(ctx, "/proc", actual.PID, device, inode)
		if e != nil || !owns {
			return before, errors.New("updater: exact updater does not own the original lock")
		}
		m, _, e := managerSnapshot(ctx, target.Unit)
		if e != nil {
			return before, e
		}
		if m["MainPID"] != fmt.Sprint(actual.PID) {
			return before, errors.New("updater: manager no longer owns the original updater")
		}
		if journal.Phase != "planned" {
			return before, errors.New("updater: original takeover signal is unconfirmed; observe it or use fresh authority")
		}
		journal.Phase = "stopping_updater"
		if err := writeJSON(boundedInstallerPath(cfg, a.RequestID), journal); err != nil {
			return before, err
		}
		// Manual stop inhibits Restart=on-failure before the exact pidfd signal.
		stopCtx, stopCancel := context.WithTimeout(ctx, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
		_, e = (systemdUser{}).call(stopCtx, "--no-block", "stop", target.Unit)
		stopCancel()
		if e != nil {
			return before, e
		}
		killCtx, killCancel := context.WithTimeout(ctx, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
		e = (LinuxProcess{}).Crash(killCtx, actual, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
		killCancel()
		if e != nil {
			return before, e
		}
	} else if !errors.Is(inspectErr, ErrNotRunning) {
		return before, inspectErr
	}
	// A completed exact process exit can be recovered with a fresh CAS even if
	// the previous installation response was lost. Another lock owner is refused.
	lockCtx, lockCancel := context.WithTimeout(ctx, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
	ownerLock, err := boundedAcquireOwnership(lockCtx, cfg, *a.UpdaterSource)
	lockCancel()
	if err != nil {
		return before, fmt.Errorf("updater: acquire original supervisor ownership: %w", err)
	}
	stopped, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		ownerLock.Close()
		return before, err
	}
	if digest(stopped) != digest(before) {
		ownerLock.Close()
		return stopped, errors.New("updater: durable source changed while stopping; fresh external CAS required")
	}
	authority, err := NewHTTPAuthority(cfg)
	if err != nil {
		ownerLock.Close()
		return stopped, err
	}
	s, err := openSupervisor(cfg, Options{Authority: authority}, ownerLock)
	if err != nil {
		return stopped, fmt.Errorf("updater: open adopted supervisor with held ownership: %w", err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	latest := s.Status()
	normalized := cloneState(latest)
	normalized.Generation = before.Generation
	normalized.UpdaterEpoch = before.UpdaterEpoch
	normalized.UpdaterBootID = before.UpdaterBootID
	expectedGeneration := before.Generation
	if before.Format >= 2 {
		expectedGeneration++
	}
	if latest.Generation != expectedGeneration || digest(normalized) != digest(before) {
		return latest, errors.New("updater: external adoption changed more than its own epoch")
	}
	originalGeneration := before.Generation
	s.externalGeneration = &originalGeneration
	if err := authorize(s); err != nil {
		return s.Status(), err
	}
	s.externalGeneration = nil
	// Preserve the original overall deadline across controller replacement.
	if s.state.Bounded != nil {
		s.state.Bounded.OverallStartedAtMillis = journal.Window.OverallStartedAtMillis
		s.state.Bounded.OverallDeadlineMillis = journal.Window.OverallDeadlineMillis
		s.state.Bounded.OverallDeadlineMono = journal.Window.OverallDeadlineMono
		if err := s.save(); err != nil {
			return s.Status(), err
		}
	}
	if err := copyPinnedAtomic(target.Candidate, target.UpdaterBinary, 0o755, target.CandidateSHA256); err != nil {
		return s.Status(), err
	}
	latest = s.Status()
	if err := s.Close(); err != nil {
		return latest, err
	}
	s = nil
	journal.Phase = "starting_updater"
	if err := writeJSON(boundedInstallerPath(cfg, a.RequestID), journal); err != nil {
		return latest, err
	}
	if _, err := (systemdUser{}).call(ctx, "start", target.Unit); err != nil {
		return latest, err
	}
	journal.Phase = "updater_started"
	if err := writeJSON(boundedInstallerPath(cfg, a.RequestID), journal); err != nil {
		return latest, err
	}
	return readState(filepath.Join(cfg.StateDir, "state.json"))
}

func validateBoundedUpdaterTarget(ctx context.Context, cfg Config, p protocol.BoundedUpdaterTarget) error {
	if p.Unit == "" || !filepath.IsAbs(p.Candidate) || !filepath.IsAbs(p.UpdaterBinary) || !filepath.IsAbs(p.ConfigFile) {
		return errors.New("updater: incomplete pinned supervisor installation target")
	}
	raw, err := privateFile(p.ConfigFile)
	if err != nil {
		return err
	}
	var fixed Config
	if err := strictJSON(raw, &fixed); err != nil {
		return err
	}
	normalizeConfig(&fixed)
	if bytesDigest(raw) != p.ConfigFileSHA256 || digest(fixed) != digest(cfg) {
		return errors.New("updater: fixed supervisor configuration changed")
	}
	if sum, e := hashFile(p.Candidate); e != nil || sum != p.CandidateSHA256 {
		return errors.New("updater: pinned supervisor candidate changed")
	}
	unit, e := unitDigest(ctx, p.Unit)
	if e != nil {
		return e
	}
	m, policy, e := managerSnapshot(ctx, p.Unit)
	if e != nil {
		return e
	}
	if unit != p.UnitSHA256 || policy != p.ManagerSHA256 || m["RefuseManualStop"] != "no" || m["KillMode"] != "process" && m["KillMode"] != "none" {
		return errors.New("updater: pinned manager or child-retention policy changed")
	}
	return nil
}

func boundedAcquireOwnership(ctx context.Context, cfg Config, previous Identity) (*os.File, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := lockDir(cfg.StateDir)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, err
		}
		// A process can lose its executable mapping before its last flock closes.
		// Wait within the existing takeover deadline; never call that gap exit proof.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("updater: exact updater exited but its ownership lock remains unavailable: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
