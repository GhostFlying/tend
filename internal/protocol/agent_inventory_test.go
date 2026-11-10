package protocol_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
)

func purposeInventoryFixture(t *testing.T, kind agentconfig.RuntimeKind, cli string) protocol.AgentInventoryProjection {
	t.Helper()
	cfg := agentconfig.Config{CapabilityVersion: 1, FormatVersion: 2,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "native", Revision: 4, Kind: kind, CLI: cli, ExecutableRef: "PRIVATE_EXECUTABLE", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}},
		Agents: []agentconfig.AgentDefinition{
			{LocalID: "agent-a", Revision: 2, DisplayName: "Agent A", RuntimeID: "native", Categories: []string{"developer"}, Capabilities: []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "configured-model", Effort: "high", Enabled: true},
			{LocalID: "agent-b", Revision: 3, DisplayName: "Agent B", RuntimeID: "native", Categories: []string{"developer"}, Capabilities: []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "configured-model", Effort: "high", Enabled: true},
		}}
	d, err := agentconfig.Validate("runner-a", cfg, agentconfig.SourceReference{Kind: agentconfig.Inline})
	if err != nil {
		t.Fatal(err)
	}
	return d.Public()
}

func TestInventoryAcceptsHistoricalPurposeMetadataAcrossActualAdapters(t *testing.T) {
	for _, tt := range []struct {
		kind agentconfig.RuntimeKind
		cli  string
	}{
		{agentconfig.AppServerWorker, "codex"},
		{agentconfig.HostedManager, "codex"},
		{agentconfig.OrcaWorker, "codex"},
		{agentconfig.CentralWorker, "traex"},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			p := purposeInventoryFixture(t, tt.kind, tt.cli)
			_, oldHash, err := protocol.CanonicalAgentInventory(p)
			if err != nil {
				t.Fatal(err)
			}
			p.Runtimes[0].DeclaredPurposes = []string{"issue_manager", "main"}
			p.Agents[0].Purposes = []string{"consultation", "retained_label"}
			p.Agents[1].Purposes = []string{"worker_run", "worker_run"}
			before, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			raw, hash, err := protocol.CanonicalAgentInventory(p)
			if err != nil || hash == oldHash {
				t.Fatal("inventory rejected or erased historical purpose metadata", err)
			}
			after, err := json.Marshal(p)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("inventory canonicalization mutated caller metadata", err)
			}
			var got protocol.AgentInventoryProjection
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Agents[0].Purposes, p.Agents[0].Purposes) || !reflect.DeepEqual(got.Agents[1].Purposes, p.Agents[1].Purposes) || !reflect.DeepEqual(got.Runtimes[0].DeclaredPurposes, p.Runtimes[0].DeclaredPurposes) {
				t.Fatal("retained purposes disappeared from public history", got)
			}
			got.Runtimes[0].DeclaredPurposes, p.Runtimes[0].DeclaredPurposes = nil, nil
			for i := range p.Agents {
				got.Agents[i].Purposes, p.Agents[i].Purposes = nil, nil
			}
			if !reflect.DeepEqual(got, p) {
				t.Fatal("purpose metadata changed configured category, capability, model, binding or availability", got, p)
			}
		})
	}
}

func TestInventoryPurposeMetadataDoesNotOverrideRuntimeAndIdentityValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*protocol.AgentInventoryProjection)
	}{
		{"adapter-kind", func(p *protocol.AgentInventoryProjection) { p.Runtimes[0].Kind = "absent-adapter" }},
		{"adapter-cli", func(p *protocol.AgentInventoryProjection) { p.Runtimes[0].CLI = "absent-cli" }},
		{"model", func(p *protocol.AgentInventoryProjection) { p.Agents[0].RequestedModel = "" }},
		{"effort", func(p *protocol.AgentInventoryProjection) { p.Agents[0].RequestedEffort = "invalid-effort" }},
		{"binding-revision", func(p *protocol.AgentInventoryProjection) { p.Agents[0].Binding.Revision++ }},
		{"foreign-environment", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].Identity.ExecutionEnvironmentID = "another-runner"
		}},
		{"duplicate-identity", func(p *protocol.AgentInventoryProjection) { p.Agents[1].Identity = p.Agents[0].Identity }},
		{"owner-binding-revision", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].OwnerBinding = &protocol.AgentRuntimeReference{RuntimeID: "native", Revision: 999}
		}},
		{"unverified-availability", func(p *protocol.AgentInventoryProjection) { p.Agents[0].Availability.Available = true }},
		{"empty-category", func(p *protocol.AgentInventoryProjection) { p.Agents[0].Categories = nil }},
		{"missing-capability-revision", func(p *protocol.AgentInventoryProjection) { p.Agents[0].Capabilities[0].DefinitionRevision = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := purposeInventoryFixture(t, agentconfig.AppServerWorker, "codex")
			p.Runtimes[0].DeclaredPurposes = []string{"main"}
			p.Agents[0].Purposes = []string{"consultation", "worker_run"}
			tt.change(&p)
			if _, _, err := protocol.CanonicalAgentInventory(p); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
				t.Fatal("historical metadata bypassed actual runtime or identity validation", err)
			}
		})
	}
}

func TestAgentInventoryUsesOnePublicDTOAndCanonicalFormat(t *testing.T) {
	cfg := agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "worker", Revision: 1, Kind: agentconfig.OrcaWorker, CLI: "codex", ExecutableRef: "PRIVATE_EXECUTABLE", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "review", Revision: 1, DisplayName: "Review", RuntimeID: "worker", Categories: []string{"reviewer", "planner"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "requested", Effort: "high", Enabled: true}}}
	d, err := agentconfig.Validate("runner-a", cfg, agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: "PRIVATE_SOURCE"})
	if err != nil {
		t.Fatal(err)
	}
	var projection protocol.AgentInventoryProjection = d.Public()
	b, hash, err := d.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	wire, wireHash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil || !bytes.Equal(b, wire) || hash != wireHash || bytes.Contains(b, []byte("PRIVATE_")) {
		t.Fatal("public contract diverged or leaked", err)
	}
	projection.Agents[0].Categories[0], projection.Agents[0].Categories[1] = projection.Agents[0].Categories[1], projection.Agents[0].Categories[0]
	if _, other, err := protocol.CanonicalAgentInventory(projection); err != nil || other != hash {
		t.Fatal("set order changed canonical hash", other, err)
	}
	projection.SourceKind = protocol.AgentSourceInline
	if _, other, err := protocol.CanonicalAgentInventory(projection); err != nil || other == hash {
		t.Fatal("typed source kind missing from hash", other, err)
	}
	projection.Agents[0].Availability.Available = true
	if _, _, err := protocol.CanonicalAgentInventory(projection); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("unverified availability accepted", err)
	}
	g := schema.New()
	for _, values := range protocol.APIEnums() {
		g.Enum(values)
	}
	for _, root := range protocol.APITypes() {
		g.Add(root)
	}
	definition, err := g.Document("TendAPI")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(definition, &document); err != nil {
		t.Fatal(err)
	}
	properties := document["properties"].(map[string]any)
	definitions := document["definitions"].(map[string]any)
	for _, name := range []string{"AgentCategory", "PutAgentCategoryPayload"} {
		if properties[name] == nil || definitions[name] == nil {
			t.Fatalf("owner category type %s is absent from actual API export roots", name)
		}
	}
	for _, field := range []string{"executable_ref", "credentials", "source_reference", "instructions"} {
		if bytes.Contains(definition, []byte(`"`+field+`"`)) {
			t.Fatalf("private field %s exported", field)
		}
	}
}
