package app

import (
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func capabilityInventory(t *testing.T, h *harness) store.AgentInventoryRow {
	t.Helper()
	var inventory store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inventory, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func TestAgentInventoryRetirementPreservesAcceptedDeclarations(t *testing.T) {
	h, r, originalPin := capabilitySetup(t)
	original := capabilityInventory(t, h)
	p := capabilityInventory(t, h).Projection
	request := func(local, server int64, snapshot string) *protocol.Envelope {
		t.Helper()
		_, hash, err := protocol.CanonicalAgentInventory(p)
		if err != nil {
			t.Fatal(err)
		}
		return inventoryRequest(h, r, p, hash, local, server, snapshot)
	}
	first := request(2, 1, "before-retirement")
	accepted := h.exec(runner("linux-1"), first, RegisterAgentInventory{})
	if accepted.Status != protocol.OpSucceeded {
		t.Fatal(accepted)
	}
	putTaskCapability(t, h, "image_gen", "retired", 1)

	// Only beta changes. Alpha retains its accepted identity/revision/declarations.
	p.Agents[1].Revision++
	update := request(3, 2, "unrelated-update")
	updated := h.exec(runner("linux-1"), update, RegisterAgentInventory{})
	if updated.Status != protocol.OpSucceeded {
		t.Fatal("retired declaration blocked unrelated update", updated)
	}
	versions, events := h.count("agent_inventory_versions"), h.count("events")
	for _, tc := range []struct {
		env  *protocol.Envelope
		want protocol.Operation
	}{{first, accepted}, {update, updated}, {request(3, 3, "unrelated-update"), updated}} {
		got := h.exec(runner("linux-1"), tc.env, RegisterAgentInventory{})
		if got.Status != protocol.OpSucceeded || !reflect.DeepEqual(got.Result, tc.want.Result) {
			t.Fatal("historical request or local snapshot replay changed receipt", got)
		}
	}
	requireInventoryCode(t, h, runner("linux-1"), request(4, 2, "stale-cas"), protocol.CodeStaleRevision)
	if h.count("agent_inventory_versions") != versions || h.count("events") != events {
		t.Fatal("replay or stale CAS changed inventory or events")
	}

	// Current inventory no longer contains alpha, but its history still reserves
	// both the immutable declaration and its eligibility to be restored unchanged.
	agents := p.Agents
	p.Agents = agents[1:]
	if op := h.exec(runner("linux-1"), request(4, 3, "remove-alpha"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p.Agents = agents
	if op := h.exec(runner("linux-1"), request(5, 4, "restore-alpha"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal("historical declaration restore rejected", op)
	}
	if got := capabilityInventory(t, h); got.Receipt.ServerRevision != 5 || !reflect.DeepEqual(got.Projection, p) {
		t.Fatal("restored inventory changed", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		old, err := store.GetAgentInventoryAt(t.Context(), tx.Q(), originalPin)
		if err == nil && !reflect.DeepEqual(old, original) {
			t.Fatal("original inventory history changed", old)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInventoryRetirementStillValidatesNewDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*protocol.AgentInventoryProjection)
		code   protocol.Code
	}{
		{"new identity", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].Identity.LocalID = "new-agent"
		}, protocol.CodeInvalidRequest},
		{"new agent revision", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].Revision++
		}, protocol.CodeInvalidRequest},
		{"other agent adds retired reference", func(p *protocol.AgentInventoryProjection) {
			p.Agents[1].Revision++
			p.Agents[1].Capabilities = p.Agents[0].Capabilities
		}, protocol.CodeInvalidRequest},
		{"new retired definition revision", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].Revision++
			p.Agents[0].Capabilities[0].DefinitionRevision = 2
		}, protocol.CodeInvalidRequest},
		{"replace under accepted revision", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].Capabilities[0].DefinitionRevision = 2
		}, protocol.CodeConflictPayload},
		{"remove under accepted revision", func(p *protocol.AgentInventoryProjection) {
			p.Agents[0].Capabilities = nil
		}, protocol.CodeConflictPayload},
		{"new active reference", func(p *protocol.AgentInventoryProjection) {
			p.Agents[1].Revision++
			p.Agents[1].Capabilities = []protocol.CapabilityReference{{CapabilityID: "render", DefinitionRevision: 1}}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, r, _ := capabilitySetup(t)
			putTaskCapability(t, h, "image_gen", "retired", 1)
			putTaskCapability(t, h, "render", "active", 0)
			before := capabilityInventory(t, h)
			// Read a separate copy so mutation cannot alter the expected snapshot.
			p := capabilityInventory(t, h).Projection
			tc.change(&p)
			_, hash, err := protocol.CanonicalAgentInventory(p)
			if err != nil {
				t.Fatal(err)
			}
			env := inventoryRequest(h, r, p, hash, 2, 1, "new-declaration")
			events, outbox := h.count("events"), h.count("outbox")
			if tc.code != "" {
				requireInventoryCode(t, h, runner("linux-1"), env, tc.code)
				requireInventoryCode(t, h, runner("linux-1"), env, tc.code)
				if !reflect.DeepEqual(capabilityInventory(t, h), before) || h.count("agent_inventory_versions") != 1 || h.count("events") != events || h.count("outbox") != outbox {
					t.Fatal("rejected declaration changed inventory, history, events or outbox")
				}
			} else {
				op := h.exec(runner("linux-1"), env, RegisterAgentInventory{})
				if op.Status != protocol.OpSucceeded || capabilityInventory(t, h).Receipt.ServerRevision != 2 {
					t.Fatal("new active declaration rejected", op)
				}
				if replay := h.exec(runner("linux-1"), env, RegisterAgentInventory{}); !reflect.DeepEqual(replay, op) {
					t.Fatal("declaration request replay changed", replay, op)
				}
			}
		})
	}
}
