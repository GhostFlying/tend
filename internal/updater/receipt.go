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

// CommittedReceipt describes only a locally persisted commit of this version.
func CommittedReceipt(s State) *protocol.UpdaterReceipt {
	proof := s.CommittedProof
	if proof == nil || proof.Version != s.Current || proof.Process != s.Process {
		return nil
	}
	b, p := proof.Binding, proof.Process
	if !validRollbackBinding(b, s.InstanceID) || b.ArtifactSHA256 != s.Current.Artifact.SHA256 || b.ReleaseID != s.Current.ReleaseID || b.ManifestSHA256 != s.Current.ManifestSHA256 || p.PID <= 0 || p.StartTicks == 0 || p.LaunchID == "" || p.SHA256 != s.Current.Artifact.SHA256 {
		return nil
	}
	return &protocol.UpdaterReceipt{Binding: b, Phase: "committed", CurrentProcessInstanceID: p.LaunchID, CurrentSHA256: p.SHA256, CommittedConfirmed: true}
}

// ProcessReceipt returns the current terminal proof, never an old version's.
func ProcessReceipt(s State) *protocol.UpdaterReceipt {
	if receipt := CommittedReceipt(s); receipt != nil {
		return receipt
	}
	return RollbackReceipt(s)
}

// CancelledReceipt proves that the specified candidate never left staging.
// An ID-only tombstone fences late requests but cannot prove a candidate's
// identity. The caller also matches the reporting process to the actual service.
func CancelledReceipt(s State, id string) *protocol.UpdaterReceipt {
	terminal, ok := s.Terminals[id]
	if !ok || terminal.Phase != "cancelled" || terminal.Binding.UpgradeID != id || !validRollbackBinding(terminal.Binding, s.InstanceID) {
		return nil
	}
	p := s.Process
	if p.PID <= 0 || p.StartTicks == 0 || p.LaunchID == "" || p.LaunchID == terminal.Binding.ProcessInstanceID || !release.ValidSHA(p.SHA256) || p.SHA256 != s.Current.Artifact.SHA256 {
		return nil
	}
	return &protocol.UpdaterReceipt{Binding: terminal.Binding, Phase: "cancelled", CurrentProcessInstanceID: p.LaunchID, CurrentSHA256: p.SHA256}
}
