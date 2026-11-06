package updater

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// completeUnestablishedBoundedFence completes only an installed force whose
// original archive fence could never be established. It performs no external
// control write and never treats an observation as native completion proof.
func (s *Supervisor) completeUnestablishedBoundedFence(ctx context.Context, a MaintenanceArchiveAuthority, p protocol.CommittedMaintenancePlan) (bool, error) {
	b, m, proof := s.state.Bounded, s.state.Maintenance, s.state.CommittedProof
	if s.cfg.Role != "server" || s.state.Format != 4 || b == nil || b.Phase != "complete" || m == nil || m.Phase != "ledger_linked" || !m.Irreversible || m.Receipt == nil || proof == nil ||
		proof.Binding != p.Successor || proof.Version != s.state.Current || proof.Version != m.Target || proof.Process != s.state.Process || proof.Process != m.Process || !validIdentity(m.Process) ||
		m.Process.LaunchID != p.Successor.ProcessInstanceID || m.Process.SHA256 != p.Successor.ArtifactSHA256 {
		return false, nil
	}
	r := m.Receipt
	if r.MaintenanceID != p.MaintenanceID || r.RequestID != p.RequestID || r.Previous != p.Previous || r.Successor != p.Successor || r.Process != m.Process || r.Epoch == "" || r.Epoch != m.Epoch ||
		r.FirstSequence == 0 || r.FirstSequence != m.FirstSequence || r.SecondSequence <= r.FirstSequence || r.SecondSequence != m.LastSequence || r.Generation == 0 || r.Generation > s.state.Generation {
		return false, nil
	}
	matches := func(auth protocol.BoundedMaintenanceAuthorization) bool {
		return auth.Role == "server" && auth.Source == p.PreviousProcess && auth.Target == p.Successor && auth.PlanSHA256 == digest(p) && protocol.ValidID(protocol.PrefixRequest, auth.RequestID) && auth.Reason != ""
	}
	current, known := b.Authorizations[b.Authorization.RequestID]
	if !known || digest(current) != digest(b.Authorization) || !matches(current) || current.ExpiresAtMillis <= s.opts.Now().UnixMilli() ||
		b.SourceExit == nil || b.SourceExit.Process != p.PreviousProcess || b.SourceExit.ConfirmedAtMillis <= 0 {
		return false, nil
	}
	var baseline *protocol.ArchiveOnlineStatus
	for _, entry := range b.Audit {
		if entry.RequestID != boundedRecoveryID(b) || (entry.Action != "force" && entry.Action != "takeover") || entry.Archive == nil || entry.ObservationError != "" {
			continue
		}
		original, ok := b.Authorizations[entry.RequestID]
		if !ok || original.RequestID != entry.RequestID || !matches(original) || original.Reason != entry.Reason || entry.RecordedAtMillis <= 0 || entry.RecordedAtMillis > b.SourceExit.ConfirmedAtMillis {
			return false, nil
		}
		baseline = entry.Archive
		break
	}
	if baseline == nil || baseline.Preparation.Prepared || p.PreparationRevision-1 >= baseline.Preparation.Revision || len(baseline.Hosts) != len(p.HostControls) {
		return false, nil
	}
	fresh, err := a.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return false, err
	}
	if fresh.Preparation != baseline.Preparation || len(fresh.Hosts) != len(p.HostControls) {
		return false, nil
	}
	controls := func(hosts []protocol.ArchiveHostStatus) (map[string]protocol.RunnerControl, bool) {
		byID := make(map[string]protocol.RunnerControl, len(hosts))
		for _, host := range hosts {
			if _, duplicate := byID[host.HostID]; duplicate || host.HostID == "" {
				return nil, false
			}
			byID[host.HostID] = host.Control
		}
		return byID, true
	}
	oldControls, oldOK := controls(baseline.Hosts)
	newControls, newOK := controls(fresh.Hosts)
	if !oldOK || !newOK {
		return false, nil
	}
	for _, host := range p.HostControls {
		old, oldExists := oldControls[host.HostID]
		now, newExists := newControls[host.HostID]
		if host.PreviousMode != "maintenance" || !oldExists || !newExists || old.Mode != "maintenance" || old.Revision != host.Revision || digest(old) != digest(now) {
			return false, nil
		}
	}
	b.Audit = append(b.Audit, protocol.BoundedMaintenanceAudit{RequestID: current.RequestID, Action: "complete_unestablished_fence", Phase: b.Phase, Reason: current.Reason, RecordedAtMillis: s.opts.Now().UnixMilli(), Archive: &fresh, Outcome: "unchanged_archive_and_host_controls_retained"})
	return true, nil
}
