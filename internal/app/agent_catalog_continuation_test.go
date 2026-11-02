package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestAgentCatalogRetainsEveryReferencedDefinitionSnapshot(t *testing.T) {
	h, r, _ := issueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	r.ContinuationBackend = "git"
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var inv store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		inv, e = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for i := range inv.Projection.Agents {
		if inv.Projection.Agents[i].Identity.LocalID == "alpha" {
			inv.Projection.Agents[i].Revision = 2
			inv.Projection.Agents[i].DisplayName = "Alpha v2"
		}
	}
	_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "second-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for i := range r.AgentExecutors {
		p := &r.AgentExecutors[i].Pin
		p.InventoryRevision = 2
		p.SnapshotID = "second-snapshot"
		p.PublicSHA256 = hash
		if p.Identity.LocalID == "alpha" {
			p.AgentRevision = 2
		}
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Another responsibility after definition reload"}, rev(issue.Revision)), RequestIssueOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	found := false
	for range 10 {
		as := ownerClaim(t, h, r)
		if as.Manager == nil {
			t.Fatalf("no manager: %+v", as)
		}
		stopOwnerClaim(t, h, as)
		if as.Manager.IssueID == issue.IssueID {
			if as.Execution.AgentRevision != 2 {
				t.Fatal(as.Execution)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("new manager never claimed")
	}
	inv.Projection.Agents = inv.Projection.Agents[1:]
	_, hash, err = protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 3, 2, "removed-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	catalog, err := h.app.RunnerAgentCatalog(t.Context(), "linux-1")
	if err != nil {
		t.Fatal(err)
	}
	versions := map[uint64]bool{}
	count := 0
	for _, history := range catalog.RetainedDefinitions {
		a := history.Definition
		if a.Identity.LocalID == "alpha" {
			versions[a.Revision] = true
			count++
			if history.Pin.AgentRevision != a.Revision || history.Pin.SnapshotID == "" || len(history.Runtimes) == 0 {
				t.Fatal("history lost its immutable runtime source", history)
			}
		}
	}
	pins := map[uint64]bool{}
	for _, p := range catalog.RetainedExecutions {
		if p.Identity.LocalID == "alpha" {
			pins[p.AgentRevision] = true
		}
	}
	raw, _ := json.Marshal(catalog.RetainedDefinitions)
	if !versions[1] || !versions[2] || count != 2 {
		t.Fatalf("retained pin versions=%v but returned definitions=%s", pins, raw)
	}
}
