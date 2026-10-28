package runnerapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAgentInventoryAuthenticatedRegistrationAndStatus(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	r := &protocol.RunnerRuntime{InstanceID: "inventory-http", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityAgentInventory}}
	p := protocol.AgentInventoryProjection{FormatVersion: 1, ExecutionEnvironmentID: "linux-1", SourceKind: protocol.AgentSourceInline, Runtimes: []protocol.AgentInventoryRuntime{}, Agents: []protocol.AgentInventoryDefinition{}}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	makeRequest := func(operation string, payload any) *protocol.Envelope {
		env := e.envelope(operation, "linux-1", payload)
		env.Target.Type = "host"
		return env
	}
	register := makeRequest(protocol.OpRunnerInventoryRegister, protocol.RegisterAgentInventoryPayload{Runtime: r, LocalRevision: 1, SnapshotID: "snapshot-http", PublicSHA256: hash, Projection: p})
	zero := int64(0)
	register.ExpectedControlRevision = &zero
	if status, res := e.call(asHost(), register); status != http.StatusConflict || res.Error == nil {
		t.Fatal("registration before hello accepted", status, res)
	}
	hello := makeRequest(protocol.OpRunnerHello, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: r})
	var result protocol.HelloResult
	if err := json.Unmarshal(e.mustOK(asHost(), hello), &result); err != nil || !result.AgentInventory {
		t.Fatal("missing inventory negotiation", result, err)
	}
	badCredential := asHost()
	badCredential.token = "wrong-token"
	if status, _ := e.call(badCredential, register); status != http.StatusForbidden {
		t.Fatal("wrong host credential accepted", status)
	}
	var receipt protocol.AgentInventoryReceipt
	if err := json.Unmarshal(e.mustOK(asHost(), register), &receipt); err != nil || receipt.ServerRevision != 1 || receipt.State != "registered_staged" {
		t.Fatalf("receipt=%+v %v", receipt, err)
	}
	read := makeRequest(protocol.OpRunnerInventoryStatus, protocol.AgentInventoryStatusPayload{Runtime: r, RequestID: register.RequestID})
	var status protocol.AgentInventoryStatus
	if err := json.Unmarshal(e.mustOK(asHost(), read), &status); err != nil || status.Request == nil || status.Request.RequestID != register.RequestID || status.Current.ServerRevision != 1 || !status.Online {
		t.Fatalf("status=%+v %v", status, err)
	}
	read.Target.ID = "other-host"
	if code, res := e.call(asHost(), read); code != http.StatusForbidden || res.Error == nil {
		t.Fatal("cross-host status accepted", code, res)
	}
}
