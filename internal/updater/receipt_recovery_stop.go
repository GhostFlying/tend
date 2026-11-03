package updater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ValidateReceiptRecoveryArchive preserves the truthful unknown lifecycle while
// requiring the previous archive protocol's other effect checks to be empty.
// It is shared by the installer/updater and the successor startup verifier.
func ValidateReceiptRecoveryArchive(p protocol.CommittedMaintenancePlan, v protocol.ArchiveOnlineStatus) error {
	if p.ReceiptRecoveryStop == nil {
		return errors.New("updater: receipt recovery requires explicitly retained unknown hosted lifecycle")
	}
	if err := maintenanceAdmission(p, v); err != nil {
		return err
	}
	pending := false
	for _, blocker := range v.Blockers {
		if blocker == "hosted operations are active or uncertain" {
			continue
		}
		count, ok := strings.CutPrefix(blocker, "native receipt reads: ")
		n, err := strconv.ParseInt(count, 10, 64)
		if !ok || err != nil || n <= 0 || pending {
			return fmt.Errorf("updater: receipt recovery cannot accept archive effect %q", blocker)
		}
		pending = true
	}
	if !pending {
		return errors.New("updater: retained native receipt uncertainty is required")
	}
	if _, err := retainedRunIdentities(p.ReceiptRecoveryStop.Runs); err != nil {
		return err
	}
	counts := map[string]int64{}
	for _, entry := range p.ReceiptRecoveryStop.Runs {
		r := entry.Run
		counts[r.Host]++
	}
	for _, host := range v.Hosts {
		if counts[host.HostID] != host.OpenRuns {
			return errors.New("updater: retained inventory differs from archive run counts")
		}
		delete(counts, host.HostID)
	}
	if len(counts) != 0 {
		return errors.New("updater: retained inventory names an unknown host")
	}
	return nil
}

// ValidateReceiptRecoveryCaptures compares complete execution identities while
// retaining each capture's independently authenticated observation metadata.
func ValidateReceiptRecoveryCaptures(p protocol.CommittedMaintenancePlan, first, second []protocol.CommittedRetainedRun) error {
	if p.ReceiptRecoveryStop == nil {
		return errors.New("updater: receipt recovery inventory required")
	}
	expected, err := retainedRunIdentities(p.ReceiptRecoveryStop.Runs)
	if err != nil {
		return err
	}
	for _, capture := range [][]protocol.CommittedRetainedRun{first, second} {
		actual, err := retainedRunIdentities(capture)
		if err != nil {
			return err
		}
		if !slices.Equal(expected, actual) {
			return errors.New("updater: complete prepared retained-run identities changed")
		}
	}
	return nil
}

type retainedRunIdentity struct {
	TaskID, RunID           string
	Role                    protocol.RunRole
	Generation, SpecVersion int64
	Host                    string
}

func retainedRunIdentities(runs []protocol.CommittedRetainedRun) ([]retainedRunIdentity, error) {
	identities := make([]retainedRunIdentity, 0, len(runs))
	seen := make(map[string]bool, len(runs))
	for _, entry := range runs {
		r := entry.Run
		if !protocol.ValidID(protocol.PrefixTask, entry.TaskID) || !protocol.ValidID(protocol.PrefixRun, r.RunID) || !r.Role.Valid() || r.Generation <= 0 || r.SpecVersion <= 0 || r.Host == "" || seen[r.RunID] {
			return nil, errors.New("updater: incomplete or invalid retained run inventory")
		}
		switch r.Observed {
		case protocol.RunStarting, protocol.RunRunning, protocol.RunWaiting, protocol.RunInterrupted, protocol.RunUnknown:
		default:
			return nil, errors.New("updater: retained inventory must contain every open run only")
		}
		seen[r.RunID] = true
		identities = append(identities, retainedRunIdentity{entry.TaskID, r.RunID, r.Role, r.Generation, r.SpecVersion, r.Host})
	}
	slices.SortFunc(identities, func(a, b retainedRunIdentity) int { return strings.Compare(a.RunID, b.RunID) })
	return identities, nil
}

type receiptRecoveryProcess interface {
	Crash(context.Context, Identity, time.Duration) error
}

// RecoverCommittedMaintenance explicitly authorizes a crash-recovery stop. It
// never reports unknown hosted lifecycle as a successfully drained observation.
func (s *Supervisor) RecoverCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, r protocol.MaintenanceReceiptRecoveryRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) || p.ReceiptRecoveryStop == nil || p.ParentMaintenanceID != "" {
		return errors.New("updater: exact root receipt recovery plan required")
	}
	if old := m.ReceiptRecoveryAuthorization; old != nil && old.RequestID == r.RequestID {
		if digest(*old) != digest(r) {
			return errors.New("updater: receipt recovery request payload changed")
		}
		return nil
	}
	if m.Phase != "updater_adopted" || r.ExpectedGeneration != s.state.Generation || !protocol.ValidID(protocol.PrefixRequest, r.RequestID) {
		return errors.New("updater: stale or invalid receipt recovery stop request")
	}
	reserved := []string{p.RequestID, p.LinkRequestID, p.PreparationRequestID, p.PreparationCancelRequestID}
	for _, h := range p.HostControls {
		reserved = append(reserved, h.RequestID, h.RestoreRequestID)
	}
	for _, id := range reserved {
		if id == r.RequestID {
			return errors.New("updater: independent receipt recovery stop request required")
		}
	}
	if err := ValidateReceiptRecoveryCaptures(p, r.FirstCapture, r.SecondCapture); err != nil {
		return err
	}
	if _, ok := s.opts.Process.(receiptRecoveryProcess); !ok {
		return errors.New("updater: exact crash recovery process support unavailable")
	}
	if err := verifyResourceEvidence(maintenanceResources(p), s.opts.Now()); err != nil {
		return err
	}
	a, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
	if !ok {
		return errors.New("updater: maintenance archive authority unavailable")
	}
	v, err := a.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return err
	}
	if err := ValidateReceiptRecoveryArchive(p, v); err != nil {
		return err
	}
	actual, err := s.opts.Process.Inspect(ctx, p.PreviousProcess.PID)
	if err != nil || actual != p.PreviousProcess {
		return ErrUnknown
	}
	found, err := s.opts.Process.Find(ctx, p.PreviousProcess.LaunchID, p.PreviousProcess.SHA256)
	if err != nil || len(found) != 1 || found[0] != actual {
		return ErrUnknown
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	remaining := p.ExpiresAtMillis - s.opts.Now().UnixMilli()
	if remaining <= 0 || remaining > (30*time.Minute).Milliseconds() {
		return errors.New("updater: receipt recovery window expired")
	}
	m.ReceiptRecoveryAuthorized = true
	m.ReceiptRecoveryRequestID = r.RequestID
	m.ReceiptRecoveryAuthorization = &r
	m.ReceiptRecoveryFirstSHA256 = digest(r.FirstCapture)
	m.ReceiptRecoverySecondSHA256 = digest(r.SecondCapture)
	m.ArchiveAuthorization = &v
	m.AuthorizationBootID = boot
	m.AuthorizationDeadlineMillis = mono + remaining
	m.Phase = "authorized"
	return s.save()
}
