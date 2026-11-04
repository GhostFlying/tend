package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type capabilityResolverFixture struct {
	observedExecutions
	calls    int
	evidence protocol.ExecutorCapabilityEvidence
}

func (f *capabilityResolverFixture) ObserveCapability(context.Context, protocol.RunExecutionPin, protocol.CapabilityReference) (protocol.ExecutorCapabilityEvidence, error) {
	f.calls++
	return f.evidence, nil
}

func TestCapabilityVerificationLostResponseReconcilesOriginalWithoutProbe(t *testing.T) {
	r := controlRunner(t)
	r.cfg.HostID = "host"
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 448)
	r.agentInventorySupported = true
	r.executorCapabilitiesSupported = true
	digest := "sha256:" + strings.Repeat("a", 64)
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "agent"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "runtime", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: digest, Profile: protocol.ExecutionProfile{Model: "model"}, Worker: protocol.WorkerOrca, BackendID: "orca"}
	ref := protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
	f := &capabilityResolverFixture{evidence: protocol.ExecutorCapabilityEvidence{Pin: pin, Capability: ref, Kind: "tool_inventory", Scope: "profile", State: "unverified", Reason: "adapter_evidence_unsupported"}}
	r.cfg.Executions = f
	r.cfg.AgentInventory = func(context.Context) (protocol.LocalAgentConfiguration, error) {
		return protocol.LocalAgentConfiguration{Revision: 1, SnapshotID: "snapshot", PublicSHA256: digest}, nil
	}
	r.confirmedRuntime = protocol.RunnerRuntime{InstanceID: "instance", Generation: 1, Capabilities: []string{protocol.CapabilityAgentInventory, protocol.CapabilityExecutorEvidence}}
	var recorded protocol.Envelope
	var accepted protocol.ExecutorCapabilityEvidence
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		var result any
		switch env.Operation {
		case protocol.OpRunnerInventoryStatus:
			result = protocol.AgentInventoryStatus{Current: &protocol.AgentInventoryReceipt{ServerRevision: 4, LocalRevision: 1, SnapshotID: "snapshot", PublicSHA256: digest}}
		case protocol.OpRunnerCapabilityEvidence:
			recorded = env
			var payload protocol.ReportCapabilityEvidencePayload
			_ = json.Unmarshal(env.Payload, &payload)
			accepted = payload.Evidence
			accepted.RequestID = env.RequestID
			accepted.EvidenceID = "evidence"
			// The server committed but the caller cannot decode its receipt.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("lost receipt"))
			return
		case protocol.OpRunnerCapabilityEvidenceStatus:
			hash, err := protocol.PayloadHash(&recorded)
			if err != nil {
				t.Error(err)
			}
			body, _ := json.Marshal(accepted)
			result = protocol.CapabilityEvidenceRequestStatus{RequestState: "recorded", RequestPayloadHash: hash, Request: &protocol.Operation{RequestID: recorded.RequestID, Status: protocol.OpSucceeded, Result: body}}
		default:
			t.Errorf("unexpected operation %s", env.Operation)
		}
		body, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: body}})
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	p := protocol.VerifyLocalCapabilityRequest{RequestID: r.cfg.IDs.New(protocol.PrefixRequest), ExpectedRevision: 1, Pin: pin, Capability: ref, ProbeKind: "tool_inventory", TimeoutMillis: 1000, MaxAttempts: 1}
	s, err := r.VerifyCapability(t.Context(), p)
	if err == nil || s.State != "report_pending" || f.calls != 1 {
		t.Fatal(s, err, f.calls)
	}
	s, err = r.VerifyCapability(t.Context(), p)
	if err != nil || s.State != "report_pending" || f.calls != 1 {
		t.Fatal("retry probed again", s, err, f.calls)
	}
	different := p
	different.RequestID = r.cfg.IDs.New(protocol.PrefixRequest)
	if _, err := r.VerifyCapability(t.Context(), different); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("unknown replaced", err)
	}
	s, err = r.ReconcileCapability(t.Context(), p.RequestID)
	if err != nil || s.State != "recorded" || s.Evidence == nil || s.Evidence.State != "unverified" || f.calls != 1 {
		t.Fatal("reconcile changed evidence or probed", s, err, f.calls)
	}
	if recorded.RequestID != p.RequestID || recorded.ExpectedControlRevision == nil || *recorded.ExpectedControlRevision != 4 {
		t.Fatal("original identity/CAS lost", recorded)
	}
}
