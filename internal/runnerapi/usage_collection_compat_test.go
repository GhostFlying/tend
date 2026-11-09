package runnerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// The frozen shapes intentionally omit both new collection fields. Unknown
// fields at either level would break a strict supported peer.
type preCollectionControl struct {
	Extensions             *protocol.ExtensionTarget               `json:"extensions,omitempty"`
	BoundedRecoveryRelease *protocol.BoundedRunnerAdmissionRelease `json:"bounded_recovery_release,omitempty"`
	CancelledUpgradeID     string                                  `json:"cancelled_upgrade_id,omitempty"`
	Upgrade                *protocol.RunnerUpgradeTarget           `json:"upgrade,omitempty"`
	Revision               int64                                   `json:"revision"`
	Mode                   string                                  `json:"mode"`
	UpgradeID              string                                  `json:"upgrade_id,omitempty"`
	ReleaseID              string                                  `json:"release_id,omitempty"`
	Authorization          *protocol.MaintenanceAuthorization      `json:"authorization,omitempty"`
}
type preCollectionHello struct {
	ManagedExtensions      bool                       `json:"managed_extensions,omitempty"`
	NativeUsage            bool                       `json:"native_usage,omitempty"`
	RolloutPreview         bool                       `json:"rollout_preview,omitempty"`
	DeploymentID           string                     `json:"deployment_id,omitempty"`
	AgentContinuation      bool                       `json:"agent_continuation,omitempty"`
	RetainedExecutions     []protocol.RunExecutionPin `json:"retained_executions,omitempty"`
	ProtocolMax            int64                      `json:"protocol_max,omitempty"`
	AgentExecution         bool                       `json:"agent_execution,omitempty"`
	AgentInventory         bool                       `json:"agent_inventory,omitempty"`
	RepositoryBindings     bool                       `json:"repository_bindings,omitempty"`
	Control                *preCollectionControl      `json:"control,omitempty"`
	LeaseTTLMillis         int64                      `json:"lease_ttl_ms"`
	Runs                   []protocol.RunVerdict      `json:"runs"`
	Reassigned             []protocol.Assignment      `json:"reassigned"`
	CapabilityDeclarations bool                       `json:"capability_declarations,omitempty"`
}

func TestUsageCollectionNegotiationPreservesStrictSupportedPeers(t *testing.T) {
	for _, version := range []int64{protocol.PreviousRunnerProtocol, protocol.CurrentRunnerProtocol} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			e := newEnv(t, time.Millisecond)
			runtime := &protocol.RunnerRuntime{InstanceID: "collection-compat", Generation: 1, Sequence: 1, Protocol: version, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: version, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityNativeUsage}}
			raw := e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1}))
			var old preCollectionHello
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&old); err != nil {
				t.Fatal("unnegotiated response changed old shape", string(raw), err)
			}
			if bytes.Contains(raw, []byte("usage_collection")) {
				t.Fatal("collection fields leaked without negotiation", string(raw))
			}
			runtime.Sequence++
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityUsageCollection)
			raw = e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1}))
			var next protocol.HelloResult
			if err := json.Unmarshal(raw, &next); err != nil || !next.UsageCollection {
				t.Fatal("new collection capability not acknowledged", next, err)
			}
		})
	}
}
