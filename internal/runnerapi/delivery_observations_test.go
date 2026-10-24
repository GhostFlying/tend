package runnerapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDeliveryObservationPollUsesRegisteredRunnerProtocol(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	runtime := &protocol.RunnerRuntime{InstanceID: "observer", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityDeliveryGitHub}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{Runtime: runtime, Held: []protocol.HeldRun{}}))
	var result protocol.DeliveryPollResult
	if err := json.Unmarshal(e.mustOK(asHost(), e.envelope(protocol.OpDeliveriesPoll, "linux-1", protocol.DeliveryPollPayload{InstanceID: "observer"})), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Assignments) != 0 {
		t.Fatal(result)
	}
}
