package updater

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/release"
)

func rollbackState() State {
	old := Version{ReleaseID: "old", Artifact: release.Artifact{SHA256: strings.Repeat("a", 64)}}
	candidate := Version{ReleaseID: "new", ManifestSHA256: strings.Repeat("b", 64), Artifact: release.Artifact{SHA256: strings.Repeat("c", 64)}}
	return State{InstanceID: "server", Current: old, Process: Identity{PID: 12, StartTicks: 2, LaunchID: "restored", SHA256: old.Artifact.SHA256}, Operation: &Operation{ID: "upgrade", Kind: "upgrade", Phase: "rolled_back", Previous: old, Candidate: candidate, LaunchID: "restored", Binding: Binding{UpgradeID: "upgrade", InstanceID: "server", ProcessInstanceID: "candidate", ReleaseID: candidate.ReleaseID, ManifestSHA256: candidate.ManifestSHA256, ArtifactSHA256: candidate.Artifact.SHA256}}}
}
func TestRollbackReceiptRequiresTerminalProof(t *testing.T) {
	s := rollbackState()
	got := RollbackReceipt(s)
	if got == nil || !got.RollbackConfirmed || got.Binding != s.Operation.Binding || got.CurrentProcessInstanceID != "restored" {
		t.Fatalf("missing completed proof: %+v", got)
	}
	tests := map[string]func(*State){
		"no operation":        func(s *State) { s.Operation = nil },
		"restore in progress": func(s *State) { s.Operation.Phase = "restoring" },
		"launch unknown":      func(s *State) { s.Operation.Phase = "rollback_starting" },
		"irreversible":        func(s *State) { s.Operation.Irreversible = true },
		"wrong version":       func(s *State) { s.Current.ReleaseID = "other" },
		"wrong process":       func(s *State) { s.Process.LaunchID = "other" },
		"missing process":     func(s *State) { s.Process.PID = 0 },
		"wrong executable":    func(s *State) { s.Process.SHA256 = strings.Repeat("d", 64) },
		"wrong candidate":     func(s *State) { s.Operation.Binding.ArtifactSHA256 = strings.Repeat("e", 64) },
		"foreign instance":    func(s *State) { s.Operation.Binding.InstanceID = "other" },
		"restart":             func(s *State) { s.Operation.Kind = "restart" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := rollbackState()
			mutate(&s)
			if RollbackReceipt(s) != nil {
				t.Fatal("unsafe state produced proof")
			}
		})
	}
}

func TestCancelledReceiptRequiresCompleteCandidateAndCurrentSelf(t *testing.T) {
	s := rollbackState()
	binding := s.Operation.Binding
	s.Terminals = map[string]TerminalReceipt{binding.UpgradeID: {Binding: binding, Phase: "cancelled"}}
	s.Operation = nil
	got := CancelledReceipt(s, binding.UpgradeID)
	if got == nil || got.Phase != "cancelled" || got.Binding != binding || got.RollbackConfirmed || got.CommittedConfirmed {
		t.Fatalf("incorrect no-start receipt: %+v", got)
	}
	partial := s
	partial.Terminals = map[string]TerminalReceipt{binding.UpgradeID: {Binding: Binding{UpgradeID: binding.UpgradeID, InstanceID: s.InstanceID}, Phase: "cancelled"}}
	if CancelledReceipt(partial, binding.UpgradeID) != nil {
		t.Fatal("ID-only tombstone treated as candidate proof")
	}
	mismatched := s
	mismatched.Process.LaunchID = binding.ProcessInstanceID
	if CancelledReceipt(mismatched, binding.UpgradeID) != nil {
		t.Fatal("candidate process treated as original service")
	}
	if CancelledReceipt(s, "different-batch") != nil {
		t.Fatal("wrong batch selected")
	}
}
