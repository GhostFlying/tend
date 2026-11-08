package app

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestExtensionPlanUsesCurrentCodexInventoryAndEffectiveChanges(t *testing.T) {
	h, runtime, projection, _ := inventorySetup(t)
	projection.FormatVersion = 2
	projection.Runtimes[0].CLI = "codex"
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, 1, 0, "first"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The latest accepted inventory no longer contains beta. It must not
	// reappear merely because historical inventory versions retain its identity.
	projection.Agents = projection.Agents[:1]
	_, hash, err = protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, 2, 1, "second"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before, _, err := extensions.NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Revision: 1, AgentsMD: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	next := before
	next.ID = "different-release"
	check := func(want int) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			affected, err := ExtensionAffectedAgents(t.Context(), tx.Q(), next, &before)
			if err != nil {
				return err
			}
			if len(affected) != want || want == 1 && affected[0] != projection.Agents[0].Identity {
				t.Fatalf("affected=%+v want=%d", affected, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(0)
	next.Catalog.Categories = []protocol.ExtensionBinding{{CategoryID: "implementer", Revision: 2, AgentsMD: "two"}}
	check(1)
	next.Catalog.Categories = nil // Removing the category layer is also a change.
	check(1)
}

func TestExtensionTargetsIncludeCodexOwnerButExcludeDisabledAgents(t *testing.T) {
	p := protocol.AgentInventoryProjection{Runtimes: []protocol.AgentInventoryRuntime{{RuntimeID: "worker", CLI: "traex", Enabled: true}, {RuntimeID: "owner", CLI: "codex", Enabled: true}}}
	a := protocol.AgentInventoryDefinition{Enabled: true, Binding: protocol.AgentRuntimeReference{RuntimeID: "worker"}}
	if managedExtensionAgent(p, a) {
		t.Fatal("Traex worker was included")
	}
	a.OwnerBinding = &protocol.AgentRuntimeReference{RuntimeID: "owner"}
	if !managedExtensionAgent(p, a) {
		t.Fatal("Codex owner was omitted")
	}
	a.Enabled = false
	if managedExtensionAgent(p, a) {
		t.Fatal("disabled Agent was included")
	}
}

func TestExtensionPublicationValidatesAgentOnlyCombinations(t *testing.T) {
	h, runtime, projection, _ := inventorySetup(t)
	projection.FormatVersion = 2
	projection.Runtimes[0].CLI = "codex"
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, 1, 0, "agent-only"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	binding := protocol.ExtensionBinding{Agent: projection.Agents[1].Identity, AgentsMD: "agent only"}
	if op := extensionEdit(t, h, "binding_set", protocol.ExtensionEditPayload{Binding: &binding}, 0); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	checked := false
	h.app.extensionValidator = func(_ context.Context, _ protocol.ExtensionRelease, _ extensions.ContentSource, uses []extensions.ValidationUse) error {
		if len(uses) != 1 || uses[0].Agent != binding.Agent || uses[0].CategoryID != "implementer" {
			t.Fatalf("Agent-only composition omitted or mixed: %+v", uses)
		}
		checked = true
		return errors.New("controlled native rejection")
	}
	op := extensionPublish(t, h)
	if !checked || op.Status == protocol.OpSucceeded || extensionState(t, h).Published != nil {
		t.Fatal("Agent-only native failure was published", op)
	}
}
