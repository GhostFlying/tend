package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func capabilityFixture() (RunExecutionPin, CapabilityReference, ExecutorCapabilityEvidence) {
	p := RunExecutionPin{Identity: AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "agent"}, AgentRevision: 1, Runtime: AgentRuntimeReference{RuntimeID: "runtime", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: ExecutionProfile{Model: "model", Effort: "high"}, Worker: WorkerOrca, BackendID: "orca"}
	r := CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
	digest := "sha256:" + strings.Repeat("b", 64)
	e := ExecutorCapabilityEvidence{EvidenceID: "evidence", Pin: p, Capability: r, InstanceID: "instance", Generation: 1, Sequence: 2, Kind: "tool_inventory", Scope: "profile", Complete: true, State: "confirmed", Reason: "tool_exposed", ObservedAtMillis: 1000, ExpiresAtMillis: 2000, Source: &CapabilityToolSource{Kind: "native", ID: "tool-registry", Version: "v1", ToolName: "image_gen__imagegen", SchemaSHA256: digest, ConfigurationSHA256: digest, ExecutableSHA256: digest}}
	return p, r, e
}

func TestCapabilityMatcherUsesOnlyExplicitRevisionDeclarations(t *testing.T) {
	p, ref, e := capabilityFixture()
	for _, evidence := range [][]ExecutorCapabilityEvidence{nil, {e}, {{Kind: "simulation"}}, {{State: "unavailable", Reason: "tool_absent"}}} {
		for _, now := range []int64{0, 1500, 3000000} {
			m := MatchExecutorCapabilities(p, []CapabilityReference{ref}, []CapabilityReference{ref}, evidence, now)
			if m.State != "eligible" || len(m.EvidenceIDs) != 0 || m.Reasons[0].Reason != "capability_declared" {
				t.Fatal(m)
			}
		}
	}
	for _, tc := range []struct {
		declared []CapabilityReference
		reason   string
	}{
		{nil, "capability_undeclared"},
		{[]CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}, "capability_revision_mismatch"},
		{[]CapabilityReference{{CapabilityID: "codex", DefinitionRevision: 1}}, "capability_undeclared"},
	} {
		m := MatchExecutorCapabilities(p, []CapabilityReference{ref}, tc.declared, []ExecutorCapabilityEvidence{e}, 1500)
		if m.State != "unavailable" || m.Reasons[0].Reason != tc.reason {
			t.Fatal(m)
		}
	}
	if m := MatchExecutorCapabilities(p, nil, nil, nil, 0); m.State != "eligible" {
		t.Fatal(m)
	}
}

func TestCapabilityReferencesCanonicalAndLegacyBytes(t *testing.T) {
	refs := []CapabilityReference{{CapabilityID: "z", DefinitionRevision: 2}, {CapabilityID: "a", DefinitionRevision: 1}}
	canonical, err := CanonicalCapabilityReferences(refs)
	if err != nil || canonical[0].CapabilityID != "a" || refs[0].CapabilityID != "z" {
		t.Fatal(canonical, err)
	}
	if !CapabilityReferencesEqual(refs, canonical) || CapabilityRequiredDigest(refs) != CapabilityRequiredDigest(canonical) {
		t.Fatal("set order changed requirements")
	}
	if _, err := CanonicalCapabilityReferences(append(refs, refs[0])); err == nil {
		t.Fatal("duplicate allowed")
	}
	r := AgentRequirement{CategoryID: "implementation"}
	body, _ := json.Marshal(r)
	if string(body) != `{"category_id":"implementation"}` {
		t.Fatal("legacy bytes changed", string(body))
	}
	s := TaskAgentSelection{Implementation: AgentRequirement{CategoryID: "implementation", RequiredCapabilities: refs}}
	clone := s.Clone()
	clone.Implementation.RequiredCapabilities[0].DefinitionRevision++
	if s.Equal(clone) {
		t.Fatal("clone mutated frozen source or comparison omitted required")
	}
}

func TestCapabilityEvidenceRejectsFabricatedStateAndProvenance(t *testing.T) {
	_, _, original := capabilityFixture()
	for _, change := range []func(*ExecutorCapabilityEvidence){
		func(e *ExecutorCapabilityEvidence) { e.Kind = "simulation" },
		func(e *ExecutorCapabilityEvidence) { e.Complete = false },
		func(e *ExecutorCapabilityEvidence) { e.Source = nil },
		func(e *ExecutorCapabilityEvidence) {
			e.ExpiresAtMillis = e.ObservedAtMillis + CapabilityEvidenceTTLMillis + 1
		},
		func(e *ExecutorCapabilityEvidence) { e.ObservedAtMillis = 1600 },
		func(e *ExecutorCapabilityEvidence) { s := *e.Source; s.ID = "private/path"; e.Source = &s },
	} {
		e := original
		change(&e)
		if e.Validate(1500) == nil {
			t.Fatalf("invalid evidence accepted: %+v", e)
		}
	}
}
