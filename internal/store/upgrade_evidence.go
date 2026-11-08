package store

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// UpgradeInstallationEvidence reads the exact historical batch and its actual
// installation receipts; a later upgrade cannot substitute for this proof.
func UpgradeInstallationEvidence(ctx context.Context, q Querier, id string, checkedAt int64) (*protocol.UpgradeEvidence, error) {
	r, err := GetUpgrade(ctx, q, id)
	if err != nil {
		return nil, err
	}
	invalid := func() (*protocol.UpgradeEvidence, error) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "upgrade evidence requires a completed batch with verified target installations")
	}
	if r.View.Status != "completed" || r.View.Phase != "completed" || r.ManifestSHA256 == "" || r.ServerHash == "" || r.View.ReleaseID != r.Manifest.ReleaseID || len(r.Hosts) != len(r.View.Hosts) {
		return invalid()
	}
	v := &protocol.UpgradeEvidence{SourceCommit: r.View.SourceCommit, UpgradeID: id, Revision: r.View.Revision, ReleaseID: r.View.ReleaseID, ManifestSHA256: r.ManifestSHA256, ServerSHA256: r.ServerHash, CheckedAt: checkedAt, CompletedAt: r.View.UpdatedAt.UnixMilli(), Hosts: []protocol.UpgradeEvidenceHost{}}
	seen := map[string]bool{}
	for i, h := range r.Hosts {
		view := r.View.Hosts[i]
		if h.HostID != view.HostID || seen[h.HostID] || h.TargetHash == "" {
			return invalid()
		}
		seen[h.HostID] = true
		switch view.Status {
		case "completed":
			p := h.Committed
			if p == nil || p.Phase != "committed" || !p.CommittedConfirmed || p.CurrentSHA256 != h.TargetHash || p.Binding.ArtifactSHA256 != h.TargetHash || p.Binding.UpgradeID != id || p.Binding.ReleaseID != r.View.ReleaseID || p.Binding.ManifestSHA256 != r.ManifestSHA256 || p.CurrentProcessInstanceID == "" {
				return invalid()
			}
		case "skipped":
			// Identical retained bytes are proven by the recorded hashes, not a
			// release label or a skipped action alone.
			if h.PreviousHash != h.TargetHash {
				return invalid()
			}
		default:
			return invalid()
		}
		v.Hosts = append(v.Hosts, protocol.UpgradeEvidenceHost{HostID: h.HostID, Status: view.Status, ArtifactSHA256: h.TargetHash, PreviousSHA256: h.PreviousHash, Receipt: h.Committed})
	}
	return v, nil
}

// DeliveryUpgradeBinding keeps the original server, complete host set and
// creation boundary independent of the deployment that happens to be latest.
func DeliveryUpgradeBinding(ctx context.Context, q Querier, target protocol.IssueDeliveryDeployment, id string, executionStarted int64) (UpgradeRow, error) {
	if id == "" {
		return UpgradeRow{}, protocol.Errorf(protocol.CodeInvalidTransition, "approved deployment requires its controlled upgrade batch")
	}
	r, err := GetUpgrade(ctx, q, id)
	if err != nil {
		return UpgradeRow{}, err
	}
	hosts := make([]string, 0, len(r.Hosts))
	for _, h := range r.Hosts {
		hosts = append(hosts, h.HostID)
	}
	slices.Sort(hosts)
	want := slices.Clone(target.HostIDs)
	slices.Sort(want)
	if r.View.CreatedAt.UnixMilli() < executionStarted || r.ServerInstance != target.ServerInstance || r.ParkIdleManagers != target.ParkIdleManagers || !slices.Equal(hosts, want) {
		return UpgradeRow{}, protocol.Errorf(protocol.CodeInvalidTransition, "upgrade must belong to this execution's approved server and complete host set")
	}
	return r, nil
}

func DeliveryUpgradeEvidence(ctx context.Context, q Querier, target protocol.IssueDeliveryDeployment, id string, executionStarted, checkedAt int64) (*protocol.UpgradeEvidence, error) {
	r, err := DeliveryUpgradeBinding(ctx, q, target, id, executionStarted)
	if err != nil {
		return nil, err
	}
	if r.ServerProcess == "" || r.ServerHealthy < 2 {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "controlled delivery requires verified installed server identity and health")
	}
	proof, err := UpgradeInstallationEvidence(ctx, q, id, checkedAt)
	if err != nil {
		return nil, err
	}
	proof.ServerInstance, proof.ServerProcessInstanceID = r.ServerInstance, r.ServerProcess
	return proof, nil
}
