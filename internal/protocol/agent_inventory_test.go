package protocol_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
)

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
