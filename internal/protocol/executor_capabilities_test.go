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

func TestCapabilityMatcherRequiresExactCurrentEvidence(t *testing.T) {
	p, r, e := capabilityFixture()
	if m := MatchExecutorCapabilities(p, []CapabilityReference{r}, []CapabilityReference{r}, []ExecutorCapabilityEvidence{e}, 1500); m.State != "eligible" || len(m.EvidenceIDs) != 1 {
		t.Fatal(m)
	}
	for _, test := range []struct {
		name, reason string
		change       func(*RunExecutionPin, *ExecutorCapabilityEvidence)
		now          int64
	}{
		{"config", "evidence_unverified", func(p *RunExecutionPin, _ *ExecutorCapabilityEvidence) {
			p.PublicSHA256 = "sha256:" + strings.Repeat("c", 64)
		}, 1500},
		{"agent", "evidence_unverified", func(p *RunExecutionPin, _ *ExecutorCapabilityEvidence) { p.AgentRevision++ }, 1500},
		{"runtime", "evidence_unverified", func(p *RunExecutionPin, _ *ExecutorCapabilityEvidence) { p.Runtime.Revision++ }, 1500},
		{"model", "evidence_unverified", func(p *RunExecutionPin, _ *ExecutorCapabilityEvidence) { p.Profile.Model = "other" }, 1500},
		{"backend", "evidence_unverified", func(p *RunExecutionPin, _ *ExecutorCapabilityEvidence) { p.BackendID = "git" }, 1500},
		{"session", "session_adoption_unsupported", func(_ *RunExecutionPin, e *ExecutorCapabilityEvidence) {
			e.Scope = "session"
			e.SessionID = "native-session"
		}, 1500},
		{"expired", "evidence_expired", func(*RunExecutionPin, *ExecutorCapabilityEvidence) {}, 2000},
		{"simulation", "simulation_only", func(_ *RunExecutionPin, e *ExecutorCapabilityEvidence) {
			e.Kind = "simulation"
			e.State = "unverified"
			e.Reason = "enumeration_incomplete"
		}, 1500},
		{"unsupported", "adapter_evidence_unsupported", func(_ *RunExecutionPin, e *ExecutorCapabilityEvidence) {
			e.State = "unverified"
			e.Reason = "adapter_evidence_unsupported"
		}, 1500},
		{"execution", "evidence_unverified", func(_ *RunExecutionPin, e *ExecutorCapabilityEvidence) {
			e.Kind = "execution"
			e.Reason = "execution_succeeded"
			e.OutputSHA256 = e.Source.SchemaSHA256
			e.CostState = "unknown"
		}, 1500},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin, evidence := p, e
			test.change(&pin, &evidence)
			m := MatchExecutorCapabilities(pin, []CapabilityReference{r}, []CapabilityReference{r}, []ExecutorCapabilityEvidence{evidence}, test.now)
			if m.State == "eligible" || m.Reasons[0].Reason != test.reason {
				t.Fatal(m)
			}
		})
	}
	if m := MatchExecutorCapabilities(p, []CapabilityReference{r}, nil, []ExecutorCapabilityEvidence{e}, 1500); m.State != "unavailable" || m.Reasons[0].Reason != "capability_undeclared" {
		t.Fatal(m)
	}
	missing := e
	missing.Sequence++
	missing.State = "unavailable"
	missing.Reason = "tool_absent"
	old := e
	old.Sequence--
	if m := MatchExecutorCapabilities(p, []CapabilityReference{r}, []CapabilityReference{r}, []ExecutorCapabilityEvidence{e, missing, old}, 1500); m.State != "unavailable" || m.Reasons[0].Reason != "tool_absent" {
		t.Fatal("late evidence overrode revocation", m)
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
