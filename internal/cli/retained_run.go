package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// retainedRunProofExample illustrates the full frozen input, not live evidence.
func retainedRunProofExample() string {
	id := protocol.RetainedRunIdentity{
		TaskID: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV", ActionID: "act_01ARZ3NDEKTSV4RRFFQ69G5FAV", Generation: 2, SpecVersion: 1,
		HostID: "runner-one", LeaseID: "lse_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Execution: protocol.RunExecutionPin{
			Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner-one", LocalID: "implementer"}, AgentRevision: 1,
			Runtime: protocol.AgentRuntimeReference{RuntimeID: "original-runtime", Revision: 1}, InventoryRevision: 1,
			SnapshotID: "original-snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64),
			Profile: protocol.ExecutionProfile{Model: "original-model", Effort: "high"}, Worker: protocol.WorkerOrca, BackendID: "orca",
		},
	}
	protections := []protocol.RetainedRunProtection{{MaintenanceID: "original-maintenance", RunID: "run_01ARZ3NDEKTSV4RRFFQ69G5FAV", TaskID: id.TaskID, ActionID: id.ActionID, HostID: id.HostID, LeaseID: id.LeaseID, OriginalRequestsJSON: "null", Linked: true}}
	raw, _ := json.Marshal(protections)
	sum := sha256.Sum256(raw)
	p := protocol.ContinueRetainedRunPayload{RetainedRunIdentity: id, Protections: protections, ProtectionSetSHA256: hex.EncodeToString(sum[:]), Reason: "Independently reconcile the original Run and attach its same native identity"}
	raw, _ = json.Marshal(p)
	return string(raw)
}
