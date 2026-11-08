package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestOwnerSelfExecutionResolvesTaskCategoryAndRetainedScope(t *testing.T) {
	h, r, p, _ := inventorySetup(t)
	p.FormatVersion = 2
	p.Runtimes[0].CLI = "codex"
	p.Agents[0].Categories = []string{"implementer", "reviewer"}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "scoped-owner"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app.extensionValidator = func(context.Context, protocol.ExtensionRelease, extensions.ContentSource, []extensions.ValidationUse) error {
		return nil
	}
	for i, category := range []string{"implementer", "reviewer"} {
		binding := protocol.ExtensionBinding{CategoryID: category, AgentsMD: category + " body"}
		if op := extensionEdit(t, h, "binding_set", protocol.ExtensionEditPayload{Binding: &binding}, int64(i)); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if op := extensionPublish(t, h); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	state := extensionState(t, h)
	var release protocol.ExtensionRelease
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		release, _, err = store.GetExtensionRelease(t.Context(), tx.Q(), state.Published.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityIssueOwner, protocol.CapabilityAgentContinuation, protocol.CapabilityManagedExtensions)
	r.JournalVersion = 19
	r.ContinuationBackend = "git"
	r.Extensions = &protocol.ExtensionHostStatus{Enabled: true, Runtimes: []string{"worker"}}
	r.AgentExecutors = nil
	for _, agent := range p.Agents {
		for _, category := range agent.Categories {
			combo, err := extensions.Resolve(release, category, agent.Identity)
			if err != nil {
				t.Fatal(err)
			}
			pin := protocol.RunExecutionPin{Extensions: combo.Pin, Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "scoped-owner", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}
			r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: pin})
			pin.Purpose = "issue_manager"
			pin.BackendID = ""
			r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: pin})
		}
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Own scoped execution"}, rev(issue.Revision)), RequestIssueOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignment := ownerClaim(t, h, r)
	if assignment.Execution == nil || assignment.Execution.Extensions.CategoryID != "implementer" {
		t.Fatal("owner category not frozen", assignment)
	}
	stopOwnerClaim(t, h, assignment)
	op = createOwnerExecution(t, h, canonicalTaskIssue(t, h, issue.IssueID), "reviewer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		contract, err := store.GetTaskOwnerContract(t.Context(), tx.Q(), task.TaskID)
		if err != nil {
			return err
		}
		expected, _ := extensions.Resolve(release, "reviewer", p.Agents[0].Identity)
		if contract.Execution == nil || contract.Execution.Extensions != expected.Pin {
			t.Fatal("self-execution inherited the owner's unrelated category", contract.Execution)
		}
		pins, err := store.RequiredAgentExecutions(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		for _, pin := range pins {
			r.AgentContinuations = append(r.AgentContinuations, protocol.AgentExecutor{Available: true, Pin: pin})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := views.HostContinuations(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		found := false
		for _, row := range rows {
			if row.Pin.Extensions != (protocol.ExtensionExecutionPin{}) {
				if len(row.Categories) != 1 || row.Categories[0] != row.Pin.Extensions.CategoryID {
					t.Fatal("retained pin offered unrelated categories", row)
				}
				if row.Pin.Purpose == "" && row.Pin.Extensions.CategoryID == "reviewer" && row.Pin.Identity.LocalID == "alpha" {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("task's retained reviewer combination is missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
