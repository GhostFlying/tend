package app

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestAgentContinuationRetainsOwnerAndSelfExecutionAfterRemoval(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	contract := ownerContract(t, h, task.TaskID)
	if contract.Execution == nil {
		t.Fatal("owner pin was not frozen")
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	r.Sequence++
	hello := h.versionedHello("linux-1", r)
	if hello.Status != protocol.OpSucceeded {
		t.Fatal(hello)
	}
	var result protocol.HelloResult
	if err := json.Unmarshal(hello.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.AgentContinuation || !slices.Contains(result.RetainedExecutions, *contract.Execution) {
		t.Fatal("self execution not retained", result)
	}
	var inv store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inv, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inv.Projection.Agents = slices.DeleteFunc(inv.Projection.Agents, func(a protocol.AgentInventoryDefinition) bool { return a.Identity.LocalID == "alpha" })
	_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "after-removal"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	inventoryCategory(t, h, "implementer", 1, "retired")
	r.AgentExecutors = nil
	for _, pin := range result.RetainedExecutions {
		r.AgentContinuations = append(r.AgentContinuations, protocol.AgentExecutor{Pin: pin, Available: true})
	}
	r.Sequence++
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r, Runs: []protocol.LeaseRef{}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(hb.RetainedExecutions, *contract.Execution) {
		t.Fatal("retention lost after category retirement")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		fresh, err := views.HostExecutors(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		if len(fresh) != 0 {
			t.Fatal("removed definition became a fresh candidate", fresh)
		}
		retained, err := views.HostContinuations(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		found := false
		for _, e := range retained {
			found = found || e.Pin == *contract.Execution
		}
		if !found {
			t.Fatal("old worker unavailable", retained)
		}
		// Retained availability has no authority to fulfill a fresh category request.
		pin, _ := scheduler.SelectExecution(scheduler.Host{ID: "linux-1", Continuations: retained}, scheduler.Candidate{Kind: protocol.ActionImplement, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}})
		if pin != nil {
			t.Fatal("continuation took fresh work")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An independent current reviewer still gates first allocation. Once it is
	// available, the ordinary claim path must hand out the retained owner pin.
	reviewer := inv.Projection.Agents[0]
	r.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{Identity: reviewer.Identity, AgentRevision: reviewer.Revision, Runtime: reviewer.Binding, InventoryRevision: 2, SnapshotID: "after-removal", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: reviewer.RequestedModel, Effort: reviewer.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}}}
	r.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
		t.Fatal(err)
	}
	assigned := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if assigned.Execution == nil || *assigned.Execution != *contract.Execution {
		t.Fatal("claim replaced the old owner snapshot", assigned)
	}
	// The owner can create work from its frozen definition even after removal.
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := createOwnerExecution(t, h, issue, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal("old owner could not prepare its work", op)
	}
}

func TestAgentContinuationRequiresMutualCapability(t *testing.T) {
	h, r, _ := issueOwnerHarness(t)
	r.Sequence++
	op := h.versionedHello("linux-1", r)
	var hello protocol.HelloResult
	if err := json.Unmarshal(op.Result, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.AgentContinuation || len(hello.RetainedExecutions) != 0 {
		t.Fatal("extension sent to old runner", hello)
	}
	r.AgentContinuations = []protocol.AgentExecutor{{Reason: "unnegotiated"}}
	r.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("unnegotiated observations accepted", err)
	}
}

func TestAgentContinuationManagerCanCreateFirstWorkerAfterAllDefinitionsRemoved(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	r.ContinuationBackend = "git"
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var inv store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inv, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inv.Projection.Agents = nil
	_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "removed-all"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.AgentExecutors = nil
	r.Sequence++
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range hb.RetainedExecutions {
		r.AgentContinuations = append(r.AgentContinuations, protocol.AgentExecutor{Pin: pin, Available: true})
	}
	r.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
		t.Fatal(err)
	}
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("retained manager could not create first self-execution despite original snapshot/adapter: %+v", op.Error)
	}
}

func TestAgentContinuationReviewerSupportsRefreshedSpecAdmission(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	r.ContinuationBackend = "git"
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["Revise implementation"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := h.storedSpec(task.TaskID, 1)
	spec.Goal += " with revised details"
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var inv store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inv, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inv.Projection.Agents = nil
	_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "removed-all"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.AgentExecutors = nil
	r.Sequence++
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range hb.RetainedExecutions {
		r.AgentContinuations = append(r.AgentContinuations, protocol.AgentExecutor{Pin: pin, Available: true})
	}
	r.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.ListClaimCandidates(t.Context(), tx.Q())
		if err != nil {
			return err
		}
		cs, err := candidatesOf(rows)
		if err != nil {
			return err
		}
		if err := applyIssueDependencies(t.Context(), tx.Q(), cs); err != nil {
			return err
		}
		retained, err := views.HostContinuations(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		for _, c := range cs {
			if c.TaskID == task.TaskID {
				pin, reason := scheduler.SelectExecution(scheduler.Host{ID: "linux-1", IssueOwners: true, ContinuationsSupported: true, Continuations: retained}, c)
				if pin == nil {
					t.Fatalf("refreshed spec rejected retained implementation+review: reason=%s named=%v owner=%v review=%v retained=%+v", reason, c.NamedExecutionAssigned, c.OwnerPin, c.ReviewOwnerPin, retained)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentContinuationReviewerCategoryRefreshUsesFrozenInventory(t *testing.T) {
	for _, supported := range []bool{true, false} {
		name := "legacy"
		if supported {
			name = "negotiated"
		}
		t.Run(name, func(t *testing.T) {
			h, r, issue := issueOwnerHarness(t)
			if supported {
				r.ContinuationBackend = "git"
				r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
			}
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			task := ownerArtifact(t, h, r, issue)
			accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
			if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["Revise implementation"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var inv store.AgentInventoryRow
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				inv, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			inventoryCategory(t, h, "new-review", 0, "active")
			for i := range inv.Projection.Agents {
				if inv.Projection.Agents[i].Identity.LocalID == "beta" {
					inv.Projection.Agents[i].Categories = append(inv.Projection.Agents[i].Categories, "new-review")
				}
			}
			_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
			if err != nil {
				t.Fatal(err)
			}
			if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "new-category"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			for i := range r.AgentExecutors {
				r.AgentExecutors[i].Pin.InventoryRevision = 2
				r.AgentExecutors[i].Pin.SnapshotID = "new-category"
				r.AgentExecutors[i].Pin.PublicSHA256 = hash
			}
			spec := h.storedSpec(task.TaskID, 1)
			spec.Goal += " revised"
			spec.AgentSelection.Acceptance.CategoryID = "new-review"
			before := h.task(task.TaskID).ControlRevision
			op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(before)), UpdateIntent{})
			if supported {
				if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
					t.Fatal("incompatible category refresh was accepted", op)
				}
				if h.task(task.TaskID).ControlRevision != before || h.storedSpec(task.TaskID, 1).AgentSelection.Acceptance.CategoryID != "reviewer" {
					t.Fatal("rejected refresh changed the contract")
				}
				spec.AgentSelection.Acceptance.CategoryID = "reviewer"
				op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(before)), UpdateIntent{})
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal("compatible refresh was rejected", op)
			}
			r.Sequence++
			hb, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r})
			if err != nil {
				t.Fatal(err)
			}
			for _, pin := range hb.RetainedExecutions {
				r.AgentContinuations = append(r.AgentContinuations, protocol.AgentExecutor{Pin: pin, Available: true})
			}
			r.Sequence++
			if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				rows, err := store.ListClaimCandidates(t.Context(), tx.Q())
				if err != nil {
					return err
				}
				cs, err := candidatesOf(rows)
				if err != nil {
					return err
				}
				if err := applyIssueDependencies(t.Context(), tx.Q(), cs); err != nil {
					return err
				}
				retained, err := views.HostContinuations(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
				if err != nil {
					return err
				}
				fresh, err := views.HostExecutors(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
				if err != nil {
					return err
				}
				matched := false
				for _, c := range cs {
					if c.TaskID == task.TaskID {
						matched = true
						pin, reason := scheduler.SelectExecution(scheduler.Host{ID: "linux-1", IssueOwners: true, ContinuationsSupported: supported, Continuations: retained, Executors: fresh}, c)
						if pin == nil {
							t.Fatalf("successful refresh created an unschedulable contract: %s; reviewer=%+v fresh=%+v retained=%+v", reason, c.ReviewOwnerPin, fresh, retained)
						}
					}
				}
				if !matched {
					t.Fatal("refreshed task was not queued")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

		})
	}
}

func TestAgentContinuationDerivesCentralWorkerFromFrozenOwnerInventory(t *testing.T) {
	h, r, _ := issueOwnerHarness(t)
	var inventory store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inventory, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	central := inventory.Projection.Runtimes[0]
	central.RuntimeID = "central"
	central.Kind = "central-traex-worker"
	central.CLI = "traex"
	central.DeclaredPurposes = []string{"worker_run"}
	inventory.Projection.Runtimes = append(inventory.Projection.Runtimes, central)
	for i := range inventory.Projection.Agents {
		a := &inventory.Projection.Agents[i]
		if a.Identity.LocalID == "alpha" {
			ownerBinding := a.Binding
			a.OwnerBinding = &ownerBinding
			a.Binding = protocol.AgentRuntimeReference{RuntimeID: central.RuntimeID, Revision: central.Revision}
		}
	}
	_, hash, err := protocol.CanonicalAgentInventory(inventory.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inventory.Projection, hash, 2, 1, "central-owner"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var manager protocol.RunExecutionPin
	for _, e := range r.AgentExecutors {
		if e.Pin.Identity.LocalID == "alpha" && e.Pin.Purpose == "issue_manager" {
			manager = e.Pin
		}
	}
	manager.InventoryRevision = 2
	manager.SnapshotID = "central-owner"
	manager.PublicSHA256 = hash
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		worker, err := store.WorkerPinForManager(t.Context(), tx.Q(), manager)
		if err != nil {
			return err
		}
		if worker.Runtime.RuntimeID != "central" || worker.Worker != protocol.WorkerOrca || worker.BackendID != "orca" || worker.InventoryRevision != 2 {
			t.Fatal("central worker did not retain its original adapter and receipt", worker)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
