package updater

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// RollbackReceipt extracts only a completed local rollback. Its caller must
// additionally bind the reporting service's actual process identity and hash.
// A partial restore, ambiguous launch, or irreversible candidate is not proof.
func RollbackReceipt(s State) *protocol.UpdaterReceipt {
	if proof := s.RollbackProof; proof != nil && proof.Version == s.Current && proof.Process == s.Process {
		b, p := proof.Binding, proof.Process
		if validRollbackBinding(b, s.InstanceID) && p.PID > 0 && p.StartTicks > 0 && p.LaunchID != "" && p.LaunchID != b.ProcessInstanceID && p.SHA256 == s.Current.Artifact.SHA256 && release.ValidSHA(p.SHA256) {
			return &protocol.UpdaterReceipt{Binding: b, Phase: "rolled_back", CurrentProcessInstanceID: p.LaunchID, CurrentSHA256: p.SHA256, RollbackConfirmed: true}
		}
	}
	return operationRollbackReceipt(s)
}
func validRollbackBinding(b Binding, instance string) bool {
	return b.UpgradeID != "" && b.InstanceID == instance && instance != "" && b.ProcessInstanceID != "" && b.ReleaseID != "" && release.ValidSHA(b.ManifestSHA256) && release.ValidSHA(b.ArtifactSHA256)
}
func operationRollbackReceipt(s State) *protocol.UpdaterReceipt {
	op := s.Operation
	if op == nil || op.Kind != "upgrade" || op.Phase != "rolled_back" || op.Irreversible || s.Current != op.Previous {
		return nil
	}
	b := op.Binding
	if b.UpgradeID == "" || b.UpgradeID != op.ID || b.InstanceID == "" || b.InstanceID != s.InstanceID || b.ProcessInstanceID == "" || b.ReleaseID == "" || !release.ValidSHA(b.ManifestSHA256) || !release.ValidSHA(b.ArtifactSHA256) {
		return nil
	}
	if b.ReleaseID != op.Candidate.ReleaseID || b.ManifestSHA256 != op.Candidate.ManifestSHA256 || b.ArtifactSHA256 != op.Candidate.Artifact.SHA256 {
		return nil
	}
	p := s.Process
	if p.PID <= 0 || p.StartTicks == 0 || p.LaunchID == "" || p.LaunchID != op.LaunchID || p.LaunchID == b.ProcessInstanceID || !release.ValidSHA(p.SHA256) || p.SHA256 != s.Current.Artifact.SHA256 {
		return nil
	}
	return &protocol.UpdaterReceipt{Binding: b, Phase: op.Phase, CurrentProcessInstanceID: p.LaunchID, CurrentSHA256: p.SHA256, RollbackConfirmed: true}
}
