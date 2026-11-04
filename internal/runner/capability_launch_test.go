package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type capabilityWorkersFixture struct {
	evidence protocol.ExecutorCapabilityEvidence
	starts   int
}

func (w *capabilityWorkersFixture) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	panic("capability launch used ordinary start")
}
func (w *capabilityWorkersFixture) CapabilityInventory(context.Context, protocol.RunExecutionPin, protocol.CapabilityReference) (protocol.ExecutorCapabilityEvidence, error) {
	return w.evidence, nil
}
func (w *capabilityWorkersFixture) StartWithCapabilities(context.Context, harness.WorkerSpec, protocol.RunCapabilityBinding) (harness.Worker, error) {
	w.starts++
	return nil, nil
}

func TestCapabilityLaunchRechecksSourceAndIncarnationBeforeBusiness(t *testing.T) {
	r := controlRunner(t)
	now := r.cfg.Now().UnixMilli()
	digest := "sha256:" + strings.Repeat("a", 64)
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "agent"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "runtime", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: digest, Profile: protocol.ExecutionProfile{Model: "model"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
	ref := protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
	e := protocol.ExecutorCapabilityEvidence{EvidenceID: "evidence", Pin: pin, Capability: ref, InstanceID: "instance", Generation: 1, Sequence: 1, Kind: "tool_inventory", Scope: "profile", Complete: true, State: "confirmed", Reason: "tool_exposed", ObservedAtMillis: now - 1, ExpiresAtMillis: now + 1000, Source: &protocol.CapabilityToolSource{Kind: "native", ID: "fixture", Version: "v1", ToolName: "image_gen__imagegen", SchemaSHA256: digest, ExecutableSHA256: digest, ConfigurationSHA256: digest}}
	binding := protocol.RunCapabilityBinding{Pin: pin, Required: []protocol.CapabilityReference{ref}, RequiredSHA256: protocol.CapabilityRequiredDigest([]protocol.CapabilityReference{ref}), EvidenceIDs: []string{e.EvidenceID}, Evidence: []protocol.ExecutorCapabilityEvidence{e}}
	as := protocol.Assignment{Execution: &pin, CapabilityBinding: &binding}
	r.confirmedRuntime = protocol.RunnerRuntime{InstanceID: "instance", Generation: 1}
	w := &capabilityWorkersFixture{evidence: e}
	start, err := r.capabilityWorkerStart(t.Context(), as, ResolvedExecution{Workers: w})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := start(t.Context(), harness.WorkerSpec{}); err != nil || w.starts != 1 {
		t.Fatal(err, w.starts)
	}
	s := *e.Source
	s.Version = "v2"
	w.evidence.Source = &s
	if _, err := r.capabilityWorkerStart(t.Context(), as, ResolvedExecution{Workers: w}); err == nil {
		t.Fatal("changed tool version launched")
	}
	w.evidence = e
	r.confirmedRuntime.Generation++
	if _, err := r.capabilityWorkerStart(t.Context(), as, ResolvedExecution{Workers: w}); err == nil {
		t.Fatal("old incarnation launched")
	}
	if w.starts != 1 {
		t.Fatal("business started after failed preflight", w.starts)
	}
}
