package updater

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// ValidateBoundedRecoveryStartup authenticates a launch hint against durable
// updater authority and the actual process. It does not classify native work.
func ValidateBoundedRecoveryStartup(state State, role string, actual protocol.RecoveryIdentity, requestID string) (*protocol.BoundedMaintenanceAuthorization, error) {
	b := state.Bounded
	if state.Format != 4 || b == nil || boundedRecoveryID(b) != requestID || b.Authorization.Role != role || b.SourceExit == nil || b.SourceExit.Process != b.Authorization.Source || b.SourceExit.ConfirmedAtMillis <= 0 || !validIdentity(actual) {
		return nil, errors.New("updater: bounded startup authority or predecessor exit is not confirmed")
	}
	a := b.Authorization
	if original, ok := b.Authorizations[boundedRecoveryID(b)]; ok {
		a = original
	}
	if actual.LaunchID != a.Target.ProcessInstanceID || actual.SHA256 != a.Target.ArtifactSHA256 || a.Target.InstanceID != state.InstanceID || !protocol.ValidID(protocol.PrefixRequest, a.RequestID) || !release.ValidSHA(a.PlanSHA256) || a.Reason == "" {
		return nil, errors.New("updater: bounded startup does not match the authorized successor")
	}
	if role == "server" {
		m := state.Maintenance
		if m == nil || m.Plan.Successor != a.Target || m.Plan.PreviousProcess != a.Source || digest(m.Plan) != a.PlanSHA256 || m.Process.PID != 0 && m.Process != actual || (m.Phase != "starting_successor" && m.Phase != "checking_successor" && m.Phase != "successor_committed" && m.Phase != "ledger_linked") {
			return nil, errors.New("updater: bounded server startup changed the original maintenance")
		}
	} else if role == "runner" {
		op := state.Operation
		if op == nil || op.Binding != a.Target || op.PreviousProcess != a.Source || op.CandidateProcess.PID != 0 && op.CandidateProcess != actual || (op.Phase != "starting" && op.Phase != "checking" && op.Phase != "committed") {
			return nil, errors.New("updater: bounded runner startup changed the authorized operation")
		}
	} else {
		return nil, errors.New("updater: unsupported bounded startup role")
	}
	return &a, nil
}

func validateTimeoutPolicy(p protocol.MaintenanceTimeoutPolicy) error {
	durations := []int64{p.HandoffMillis, p.FenceMillis, p.DrainMillis, p.StopMillis, p.KillMillis, p.BackupMillis, p.StartHealthMillis, p.CompleteMillis}
	var sum int64
	for _, n := range durations {
		if n <= 0 || n > p.OverallMillis {
			return errors.New("updater: every maintenance phase needs a positive bounded timeout")
		}
		sum += n
	}
	if p.OverallMillis <= 0 || p.OverallMillis > (30*time.Minute).Milliseconds() || sum > p.OverallMillis {
		return errors.New("updater: maintenance phase budgets exceed the fixed overall deadline")
	}
	return nil
}

func (s *Supervisor) boundedEnter(phase string, budget int64) error {
	b := s.state.Bounded
	if b == nil || b.Phase == phase {
		return nil
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	if boot != b.BootID {
		return errors.New("updater: bounded recovery requires fresh external authority after reboot")
	}
	now := s.opts.Now().UnixMilli()
	deadline := min(now+budget, b.OverallDeadlineMillis)
	b.Phase, b.PhaseEnteredAtMillis, b.PhaseDeadlineMillis = phase, now, deadline
	b.PhaseDeadlineMono = min(mono+budget, b.OverallDeadlineMono)
	return s.save()
}

func (s *Supervisor) boundedRemaining() (time.Duration, error) {
	b := s.state.Bounded
	if b == nil {
		return 0, nil
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return 0, err
	}
	remaining := min(b.PhaseDeadlineMillis-s.opts.Now().UnixMilli(), b.OverallDeadlineMillis-s.opts.Now().UnixMilli(), b.PhaseDeadlineMono-mono, b.OverallDeadlineMono-mono)
	if boot != b.BootID || remaining <= 0 {
		recorded := false
		for _, entry := range b.Audit {
			w := entry.PreviousWindow
			if entry.RequestID == b.Authorization.RequestID && entry.Action == "deadline_exceeded" && w != nil && w.Phase == b.Phase && w.PhaseDeadlineMono == b.PhaseDeadlineMono && w.OverallDeadlineMono == b.OverallDeadlineMono {
				recorded = true
				break
			}
		}
		if !recorded {
			snapshot := boundedWindowSnapshot(b)
			if b.Outcome == "running" {
				b.Outcome = "timed_out"
			}
			b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "deadline_exceeded", Phase: b.Phase, Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "timed_out", PreviousWindow: snapshot})
		}
		return 0, fmt.Errorf("updater: bounded maintenance deadline exceeded in %s; explicit external recovery required", b.Phase)
	}
	return time.Duration(remaining) * time.Millisecond, nil
}

// AuthorizeBoundedMaintenance records an independent force decision without
// changing the original plan, drain observation, or receipt-recovery verdict.
// PlanSHA256 is SHA256(json.Marshal(the original CommittedMaintenancePlan)).
func (s *Supervisor) AuthorizeBoundedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, a protocol.BoundedMaintenanceAuthorization, action string) error {
	return s.authorizeBoundedMaintenanceWindow(ctx, p, a, action, nil)
}

func (s *Supervisor) authorizeBoundedMaintenanceWindow(ctx context.Context, p protocol.CommittedMaintenancePlan, a protocol.BoundedMaintenanceAuthorization, action string, window *protocol.BoundedMaintenanceState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) || a.Role != "server" || a.Source != p.PreviousProcess || a.Target != p.Successor || a.PlanSHA256 != digest(p) || !protocol.ValidID(protocol.PrefixRequest, a.RequestID) || a.RequestID == p.RequestID || a.RequestID == p.LinkRequestID || a.Reason == "" || a.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
		return errors.New("updater: exact independent bounded maintenance authority required")
	}
	if err := validateTimeoutPolicy(a.Timeouts); err != nil {
		return err
	}
	for _, history := range s.state.BoundedHistory {
		if _, ok := history.Authorizations[a.RequestID]; ok {
			return errors.New("updater: historical bounded request cannot authorize another maintenance")
		}
	}
	if b := s.state.Bounded; b != nil {
		if old, ok := b.Authorizations[a.RequestID]; ok {
			if digest(old) != digest(a) {
				return errors.New("updater: bounded request payload changed")
			}
			for _, entry := range b.Audit {
				if entry.RequestID == a.RequestID {
					if entry.Action != action {
						return errors.New("updater: bounded request action changed")
					}
					break
				}
			}
			return nil
		}
	}
	originalHandoff := a.ExpectedPhase == "handoff" && a.ExpectedGeneration == p.ExpectedGeneration && m.Phase == "updater_adopted"
	if !originalHandoff && (!s.boundedGenerationMatches(a.ExpectedGeneration) || a.ExpectedPhase != m.Phase) {
		return errors.New("updater: stale bounded maintenance generation or phase")
	}
	if action != "force" && action != "extend" && action != "apply" && action != "takeover" {
		return errors.New("updater: invalid bounded authority action")
	}
	if m.Phase != "updater_adopted" && action != "extend" && action != "takeover" {
		return errors.New("updater: a new authority cannot repeat an already started maintenance action")
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	now := s.opts.Now().UnixMilli()
	requiredUntil := now + a.Timeouts.OverallMillis
	if window != nil {
		requiredUntil = window.OverallDeadlineMillis
	}
	if a.ExpiresAtMillis < requiredUntil {
		return errors.New("updater: force authority must cover its entire fixed recovery window")
	}
	if window != nil {
		if digest(window.Authorization) != digest(a) || window.BootID != boot || window.OverallStartedAtMillis <= 0 || window.OverallDeadlineMillis-window.OverallStartedAtMillis != a.Timeouts.OverallMillis || window.OverallDeadlineMillis <= now || window.OverallDeadlineMono <= mono || window.OverallDeadlineMillis > a.ExpiresAtMillis {
			return errors.New("updater: original installer deadline or authorization changed")
		}
	}
	b := s.state.Bounded
	previousWindow := boundedWindowSnapshot(b)
	if b == nil {
		b = &protocol.BoundedMaintenanceState{RecoveryAuthorizationID: a.RequestID, Authorizations: map[string]protocol.BoundedMaintenanceAuthorization{}}
		s.state.Bounded = b
	}
	if b.Authorizations == nil {
		b.Authorizations = map[string]protocol.BoundedMaintenanceAuthorization{}
	}
	b.Authorizations[a.RequestID] = a
	b.Authorization = a
	b.OverallStartedAtMillis = now
	b.OverallDeadlineMillis = now + a.Timeouts.OverallMillis
	b.BootID = boot
	b.OverallDeadlineMono = mono + a.Timeouts.OverallMillis
	if window != nil {
		b.OverallStartedAtMillis = window.OverallStartedAtMillis
		b.OverallDeadlineMillis = window.OverallDeadlineMillis
		b.OverallDeadlineMono = window.OverallDeadlineMono
	}

	priorPhase := b.Phase
	b.Phase = "observation_drain"
	b.PhaseEnteredAtMillis = now
	b.PhaseDeadlineMillis = min(now+a.Timeouts.DrainMillis, b.OverallDeadlineMillis)
	b.PhaseDeadlineMono = min(mono+a.Timeouts.DrainMillis, b.OverallDeadlineMono)
	b.Outcome = "running"
	audit := protocol.BoundedMaintenanceAudit{PreviousWindow: previousWindow, RequestID: a.RequestID, Action: action, Phase: m.Phase, Reason: a.Reason, RecordedAtMillis: now, Outcome: "authorized_unknown_work_retained"}
	if authority, ok := s.opts.Authority.(MaintenanceArchiveAuthority); ok {
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		v, e := authority.MaintenanceArchiveStatus(readCtx)
		cancel()
		if e != nil {
			audit.ObservationError = e.Error()
		} else {
			audit.Archive = &v
			m.ArchiveAuthorization = &v
		}
	} else {
		audit.ObservationError = "maintenance archive observation unavailable"
	}
	b.Audit = append(b.Audit, audit)
	s.state.Format = 4
	if action == "force" || action == "takeover" && m.Phase == "updater_adopted" {
		m.Phase = "authorized"
		b.Phase = "stop"
		b.PhaseDeadlineMillis = now + a.Timeouts.StopMillis
		b.PhaseDeadlineMono = mono + a.Timeouts.StopMillis
	}
	if action == "extend" || action == "takeover" && m.Phase != "authorized" {
		b.Phase = priorPhase
		if b.Phase == "" {
			b.Phase = "stop"
		}
		b.PhaseDeadlineMillis = now + boundedPhaseBudget(a.Timeouts, b.Phase)
		b.PhaseDeadlineMono = mono + boundedPhaseBudget(a.Timeouts, b.Phase)
	}
	m.AuthorizationBootID = boot
	m.AuthorizationDeadlineMillis = b.OverallDeadlineMono
	return s.save()
}

func (s *Supervisor) boundedMaintenanceStep(ctx context.Context) (bool, error) {
	b := s.state.Bounded
	if b == nil || b.Authorization.Role != "server" {
		return false, nil
	}
	m := s.state.Maintenance
	if m == nil || m.Phase == "cancelled" || m.Phase == "ledger_linked" {
		return false, nil
	}
	if b.Phase == "observation_drain" && m.Phase == "updater_adopted" {
		authority, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
		var observationErr error
		if ok {
			readCtx, cancel := context.WithTimeout(ctx, time.Second)
			v, err := authority.MaintenanceArchiveStatus(readCtx)
			cancel()
			observationErr = err
			if err == nil {
				observationErr = maintenanceDrain(m.Plan, v)
				if observationErr == nil || m.ArchiveAuthorization == nil {
					m.ArchiveAuthorization = &v
				}
			}
		} else {
			observationErr = errors.New("updater: archive observation unavailable")
		}
		if observationErr != nil {
			if _, err := s.boundedRemaining(); err == nil {
				return true, observationErr
			}
			if !b.Authorization.ForceOnDrainTimeout {
				return true, errors.New("updater: drain deadline exceeded; explicit force required")
			}
			b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "force_on_drain_timeout", Phase: b.Phase, Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Archive: m.ArchiveAuthorization, ObservationError: observationErr.Error(), Outcome: "unknown_work_retained"})
		}
		m.Phase = "authorized"
		if err := s.boundedEnter("stop", b.Authorization.Timeouts.StopMillis); err != nil {
			return true, err
		}
	}
	phase, budget := "", int64(0)
	switch m.Phase {
	case "authorized", "stopping_old":
		phase, budget = "stop", b.Authorization.Timeouts.StopMillis
	case "old_exited":
		phase, budget = "backup", b.Authorization.Timeouts.BackupMillis
	case "backed_up", "starting_successor", "checking_successor":
		phase, budget = "start_health", b.Authorization.Timeouts.StartHealthMillis
	case "successor_committed":
		phase, budget = "complete", b.Authorization.Timeouts.CompleteMillis
	}
	if phase != "" && !(b.Phase == "kill" && m.Phase == "stopping_old") {
		if err := s.boundedEnter(phase, budget); err != nil {
			return true, err
		}
	}
	if _, err := s.boundedRemaining(); err != nil {
		// An explicit force decision permits one exact-process escalation after the
		// persisted graceful deadline. A lost kill reply only permits observation.
		if m.Phase == "stopping_old" && b.Phase == "stop" {
			if err := s.boundedEnter("kill", b.Authorization.Timeouts.KillMillis); err != nil {
				return true, err
			}
			killRemaining, remainingErr := s.boundedRemaining()
			if remainingErr != nil {
				return true, remainingErr
			}
			crash, ok := s.opts.Process.(receiptRecoveryProcess)
			if !ok {
				return true, errors.New("updater: exact forced termination unavailable")
			}
			b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: b.Authorization.RequestID, Action: "kill_predecessor", Phase: "kill", Reason: b.Authorization.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "started"})
			if err := s.save(); err != nil {
				return true, err
			}
			killCtx, cancel := context.WithTimeout(ctx, killRemaining)
			defer cancel()
			if err := crash.Crash(killCtx, m.Plan.PreviousProcess, killRemaining); err != nil {
				return true, err
			}
			return true, s.maintenanceRecordOldExit(ctx, m)
		}
		return true, err
	}
	return false, nil
}

func boundedPhaseBudget(p protocol.MaintenanceTimeoutPolicy, phase string) int64 {
	switch phase {
	case "handoff":
		return p.HandoffMillis
	case "fence":
		return p.FenceMillis
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
	default:
		return p.CompleteMillis
	}
}

func boundedRecoveryID(b *protocol.BoundedMaintenanceState) string {
	if b.RecoveryAuthorizationID != "" {
		return b.RecoveryAuthorizationID
	}
	return b.Authorization.RequestID
}
func boundedWindowSnapshot(b *protocol.BoundedMaintenanceState) *protocol.BoundedMaintenanceWindowSnapshot {
	if b == nil {
		return nil
	}
	return &protocol.BoundedMaintenanceWindowSnapshot{Phase: b.Phase, PhaseEnteredAtMillis: b.PhaseEnteredAtMillis, PhaseDeadlineMillis: b.PhaseDeadlineMillis, OverallStartedAtMillis: b.OverallStartedAtMillis, OverallDeadlineMillis: b.OverallDeadlineMillis, BootID: b.BootID, PhaseDeadlineMono: b.PhaseDeadlineMono, OverallDeadlineMono: b.OverallDeadlineMono, Outcome: b.Outcome}
}

func (s *Supervisor) boundedGenerationMatches(expected uint64) bool {
	return expected == s.state.Generation || s.externalGeneration != nil && expected == *s.externalGeneration
}

// ValidateBoundedMaintenanceParent authenticates retained force history when a
// failed successor needs a separately authorized forward candidate. It does not
// grant that candidate permission to stop or start any process.
func ValidateBoundedMaintenanceParent(state State, parent CommittedMaintenance) error {
	if state.Format != 4 || !parent.Irreversible {
		return errors.New("updater: bounded parent history unavailable")
	}
	matches := func(b *protocol.BoundedMaintenanceState) bool {
		if b == nil {
			return false
		}
		a := b.Authorization
		return a.Role == "server" && a.Source == parent.Plan.PreviousProcess && a.Target == parent.Plan.Successor && a.PlanSHA256 == digest(parent.Plan) && protocol.ValidID(protocol.PrefixRequest, a.RequestID) && a.Reason != "" && b.SourceExit != nil && b.SourceExit.Process == a.Source && b.SourceExit.ConfirmedAtMillis > 0
	}
	if matches(state.Bounded) {
		return nil
	}
	for _, b := range state.BoundedHistory {
		if matches(&b) {
			return nil
		}
	}
	return errors.New("updater: exact bounded parent authorization or predecessor exit missing")
}

// CancelBoundedMaintenance preserves the independent cancellation request and
// its CAS before restoring the original preparation requests. It cannot stop a
// service or turn an already launched candidate into a rollback.
func (s *Supervisor) CancelBoundedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, a protocol.BoundedMaintenanceAuthorization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) || a.Role != "server" || a.Source != p.PreviousProcess || a.Target != p.Successor || a.PlanSHA256 != digest(p) || !protocol.ValidID(protocol.PrefixRequest, a.RequestID) || a.Reason == "" {
		return errors.New("updater: exact bounded cancellation authority required")
	}
	b := s.state.Bounded
	replay := false
	if b != nil {
		if previous, ok := b.Authorizations[a.RequestID]; ok {
			if digest(previous) != digest(a) {
				return errors.New("updater: cancellation request payload changed")
			}
			for _, entry := range b.Audit {
				if entry.RequestID == a.RequestID {
					if entry.Action != "cancel" {
						return errors.New("updater: cancellation reused another action identity")
					}
					replay = true
					break
				}
			}
		}
	}
	if !replay {
		if err := validateTimeoutPolicy(a.Timeouts); err != nil {
			return err
		}
		previousWindow := boundedWindowSnapshot(b)
		boot, mono, err := s.opts.BootClock()
		if err != nil {
			return err
		}
		now := s.opts.Now().UnixMilli()
		if !s.boundedGenerationMatches(a.ExpectedGeneration) || a.ExpectedPhase != m.Phase || a.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
			return errors.New("updater: stale bounded cancellation authority")
		}
		if m.Phase != "updater_adopted" && m.Phase != "authorized" && m.Phase != "cancelling" {
			return errors.New("updater: launched maintenance requires forward recovery")
		}
		if b == nil {
			b = &protocol.BoundedMaintenanceState{RecoveryAuthorizationID: a.RequestID, Authorizations: map[string]protocol.BoundedMaintenanceAuthorization{}}
			s.state.Bounded = b
		}
		if b.Authorizations == nil {
			b.Authorizations = map[string]protocol.BoundedMaintenanceAuthorization{}
		}
		b.Authorizations[a.RequestID] = a
		b.Authorization = a
		b.BootID = boot
		b.OverallStartedAtMillis = now
		b.OverallDeadlineMillis = min(now+a.Timeouts.OverallMillis, a.ExpiresAtMillis)
		b.OverallDeadlineMono = mono + b.OverallDeadlineMillis - now
		b.Phase = "cancel"
		b.PhaseEnteredAtMillis = now
		b.PhaseDeadlineMillis = min(now+a.Timeouts.CompleteMillis, b.OverallDeadlineMillis)
		b.PhaseDeadlineMono = min(mono+a.Timeouts.CompleteMillis, b.OverallDeadlineMono)
		b.Outcome = "running"
		b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: a.RequestID, Action: "cancel", Phase: m.Phase, Reason: a.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Outcome: "cancellation_authorized", PreviousWindow: previousWindow})
		s.state.Format = 4
		if err := s.save(); err != nil {
			return err
		}
	}
	if m.Phase == "cancelled" {
		return nil
	}
	timeout, err := s.boundedRemaining()
	if err != nil {
		if digest(s.state) != digest(s.durable) {
			if e := s.save(); e != nil {
				return e
			}
		}
		return err
	}
	cancelCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.cancelCommittedMaintenanceLocked(cancelCtx, p, s.state.Generation); err != nil {
		return err
	}
	b.Outcome = "cancelled"
	return s.save()
}
