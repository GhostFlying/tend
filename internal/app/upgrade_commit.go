package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The immutable binding identifies the originally authorized candidate. A
// supervisor may connect that binding to a later incarnation of the same build.
func committedMatches(r store.UpgradeRow, h store.UpgradeHost, receipt *protocol.UpdaterReceipt, rt protocol.RunnerRuntime) bool {
	return receipt != nil && receipt.Phase == "committed" && receipt.CommittedConfirmed && !receipt.RollbackConfirmed &&
		bindingMatches(r, receipt.Binding) && receipt.Binding.InstanceID == h.HostID &&
		h.CandidateInstance != "" && receipt.Binding.ProcessInstanceID == h.CandidateInstance &&
		receipt.Binding.ArtifactSHA256 == h.TargetHash && receipt.CurrentSHA256 == h.TargetHash &&
		rt.Build.BinarySHA256 == h.TargetHash && rt.InstanceID != "" && receipt.CurrentProcessInstanceID == rt.InstanceID
}

func runnerCandidateMatches(r store.UpgradeRow, h store.UpgradeHost, rt protocol.RunnerRuntime) bool {
	return rt.InstanceID == h.CandidateInstance || committedMatches(r, h, rt.UpdaterReceipt, rt)
}
