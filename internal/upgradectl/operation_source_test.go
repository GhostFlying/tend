package upgradectl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestCommittedMaintenanceSourceRequiresTransferredOriginalOperation(t *testing.T) {
	previous := protocol.UpgradeBinding{UpgradeID: "unfinished", InstanceID: "server", ProcessInstanceID: "failed-launch", ArtifactSHA256: strings.Repeat("a", 64)}
	op := updater.Operation{ID: previous.UpgradeID, Kind: "upgrade", Phase: "checking", LaunchID: previous.ProcessInstanceID, Binding: previous, Candidate: updater.Version{Protocol: 2, StorageVersion: 101}}
	op.Candidate.Artifact.SHA256 = previous.ArtifactSHA256
	raw, _ := json.Marshal(&op)
	sum := sha256.Sum256(raw)
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "new-maintenance", Previous: previous, SourceOperation: &protocol.MaintenanceOperationSource{OperationID: op.ID, Kind: op.Kind, Phase: op.Phase, LaunchID: op.LaunchID, Protocol: 2, StorageVersion: 101, OperationSHA256: hex.EncodeToString(sum[:])}}
	st := updater.State{Format: 5, OperationHistory: map[string]updater.Operation{op.ID: op}, Maintenance: &updater.CommittedMaintenance{Plan: p}}
	if err := verifyMaintenanceParent(st, p); err != nil {
		t.Fatal("exact transferred unfinished operation not accepted", err)
	}
	for _, change := range []string{"old_writer", "active", "snapshot", "candidate"} {
		t.Run(change, func(t *testing.T) {
			copy := st
			copy.OperationHistory = map[string]updater.Operation{op.ID: op}
			switch change {
			case "old_writer":
				copy.Format = 4
			case "active":
				copy.Operation = &op
			case "snapshot":
				v := op
				v.Phase = "committed"
				copy.OperationHistory[op.ID] = v
			case "candidate":
				v := op
				v.Candidate.StorageVersion--
				copy.OperationHistory[op.ID] = v
			}
			if err := verifyMaintenanceParent(copy, p); err == nil {
				t.Fatal("changed source ownership accepted")
			}
		})
	}
}
