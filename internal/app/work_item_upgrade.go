package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func workItemUpgradeEvidence(ctx context.Context, x *Exec, q store.Querier, id string) (*protocol.UpgradeEvidence, error) {
	r, err := store.GetUpgrade(ctx, q, id)
	if err != nil {
		return nil, errNotFound(err, "upgrade batch does not exist")
	}
	invalid := func() (*protocol.UpgradeEvidence, error) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "upgrade evidence requires a completed batch with verified target installations")
	}
	if r.View.Status != "completed" || r.View.Phase != "completed" || r.ManifestSHA256 == "" || r.ServerHash == "" || r.View.ReleaseID != r.Manifest.ReleaseID || len(r.Hosts) != len(r.View.Hosts) {
		return invalid()
	}
	v := &protocol.UpgradeEvidence{UpgradeID: id, Revision: r.View.Revision, ReleaseID: r.View.ReleaseID, ManifestSHA256: r.ManifestSHA256, ServerSHA256: r.ServerHash, CheckedAt: x.Now.UnixMilli(), CompletedAt: r.View.UpdatedAt.UnixMilli(), Hosts: []protocol.UpgradeEvidenceHost{}}
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
			// A release may reuse identical runner bytes with an older embedded
			// version label. The verified installed artifact is the identity.
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
