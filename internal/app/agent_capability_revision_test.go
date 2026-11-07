package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestAgentInventoryCapabilityDeclarationRevisionFence(t *testing.T) {
	one := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	two := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}
	for _, tc := range []struct {
		name          string
		before, after []protocol.CapabilityReference
		stripVersion  bool
	}{{"add", nil, one, false}, {"remove", one, nil, false}, {"definition revision", one, two, false}, {"remove extension", one, nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, r, p, _ := inventorySetup(t)
			r.JournalVersion = 19
			r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityExecutorDeclarations)
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op := h.exec(owner, h.env("capabilities.put", protocol.Target{Type: "capability", ID: "image_gen"}, protocol.PutCapabilityPayload{Title: "Image", State: "active", ContractVersion: 1, VerificationPolicy: "declaration_v1"}, rev(1)), PutCapability{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			p.CapabilityVersion = 1
			p.Agents[0].Capabilities = tc.before
			request := func(local, server int64, snapshot string) *protocol.Envelope {
				t.Helper()
				_, hash, err := protocol.CanonicalAgentInventory(p)
				if err != nil {
					t.Fatal(err)
				}
				return inventoryRequest(h, r, p, hash, local, server, snapshot)
			}
			first := request(1, 0, "first")
			accepted := h.exec(runner("linux-1"), first, RegisterAgentInventory{})
			if accepted.Status != protocol.OpSucceeded {
				t.Fatal(accepted)
			}
			events := h.count("events")
			p.Agents[0].Capabilities = tc.after
			if tc.stripVersion {
				p.CapabilityVersion = 0
			}
			bad := request(2, 1, "bad")
			requireInventoryCode(t, h, runner("linux-1"), bad, protocol.CodeConflictPayload)
			requireInventoryCode(t, h, runner("linux-1"), bad, protocol.CodeConflictPayload)
			if h.count("agent_inventory_versions") != 1 || h.count("events") != events {
				t.Fatal("rejected registration changed inventory or events")
			}
			p.Agents[0].Revision++
			bump := request(2, 1, "bump")
			op := h.exec(runner("linux-1"), bump, RegisterAgentInventory{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if replay := h.exec(runner("linux-1"), bump, RegisterAgentInventory{}); !bytes.Equal(op.Result, replay.Result) {
				t.Fatal("bumped request replay changed", replay)
			}
			if replay := h.exec(runner("linux-1"), first, RegisterAgentInventory{}); !bytes.Equal(accepted.Result, replay.Result) {
				t.Fatal("original registration replay changed", replay)
			}
			// Removing the Agent from current inventory must not release its revision.
			agents := p.Agents
			p.Agents = nil
			if op := h.exec(runner("linux-1"), request(3, 2, "removed"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			p.Agents = agents
			p.Agents[0].Revision = 1
			requireInventoryCode(t, h, runner("linux-1"), request(4, 3, "reused"), protocol.CodeConflictPayload)
			p.Agents[0].Capabilities = tc.before
			p.CapabilityVersion = 1
			if op := h.exec(runner("linux-1"), request(4, 3, "restored"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
				t.Fatal("matching historical declaration rejected", op)
			}
			var receipt protocol.AgentInventoryReceipt
			if err := json.Unmarshal(accepted.Result, &receipt); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				old, err := store.GetAgentInventorySnapshot(t.Context(), tx.Q(), "linux-1", receipt.SnapshotID, receipt.PublicSHA256)
				if err == nil && !protocol.CapabilityReferencesEqual(old.Projection.Agents[0].Capabilities, tc.before) {
					t.Fatal("historical declaration mutated", old.Projection)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
