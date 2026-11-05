package protocol_test

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAgentCapabilityRevisionSetAndIdentitySemantics(t *testing.T) {
	image := protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
	render := protocol.CapabilityReference{CapabilityID: "render", DefinitionRevision: 2}
	a := protocol.AgentInventoryDefinition{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "env-a", LocalID: "agent-a"}, Revision: 1, Capabilities: []protocol.CapabilityReference{image, render}}
	for _, tc := range []struct {
		name string
		edit func(*protocol.AgentInventoryDefinition)
	}{
		{"order", func(b *protocol.AgentInventoryDefinition) {
			b.Capabilities = []protocol.CapabilityReference{render, image}
		}},
		{"other environment", func(b *protocol.AgentInventoryDefinition) {
			b.Identity.ExecutionEnvironmentID = "env-b"
			b.Capabilities = nil
		}},
		{"other Agent", func(b *protocol.AgentInventoryDefinition) { b.Identity.LocalID = "agent-b"; b.Capabilities = nil }},
		{"new revision", func(b *protocol.AgentInventoryDefinition) { b.Revision++; b.Capabilities = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := a
			tc.edit(&b)
			if err := protocol.ValidateAgentCapabilityRevisions([]protocol.AgentInventoryDefinition{a}, []protocol.AgentInventoryDefinition{b}); err != nil {
				t.Fatal("independent identity or equivalent set rejected", err)
			}
		})
	}
	a.Capabilities = nil
	b := a
	b.Capabilities = []protocol.CapabilityReference{}
	if err := protocol.ValidateAgentCapabilityRevisions([]protocol.AgentInventoryDefinition{a}, []protocol.AgentInventoryDefinition{b}); err != nil {
		t.Fatal("legacy nil and explicit empty diverged", err)
	}
}
