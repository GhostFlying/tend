package app

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Both agents qualify for each ordinary role and for owner execution. Keeping
// the actual inventory/hello path catches ordering lost in the claim boundary.
func fairAssignmentHarness(t *testing.T) (*harness, *protocol.RunnerRuntime) {
	t.Helper()
	h, r, p, _ := inventorySetup(t)
	p.FormatVersion = 2
	p.Runtimes[0].CLI = "codex"
	for i := range p.Agents {
		p.Agents[i].Categories = []string{"implementer", "reviewer"}
	}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "fair-inventory"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityIssueOwner)
	r.JournalVersion = 13
	for _, a := range p.Agents {
		pin := protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "fair-inventory", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: pin})
		pin.Purpose, pin.BackendID = "issue_manager", ""
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: pin})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, r
}

func reverseExecutors(n int, swap func(int, int)) {
	for i := 0; i < n/2; i++ {
		swap(i, n-1-i)
	}
}

func TestFairFirstAssignmentAndClaimReplay(t *testing.T) {
	for _, role := range []string{"owner", "worker"} {
		for _, reverse := range []bool{false, true} {
			name := role + "/forward"
			if reverse {
				name = role + "/reverse"
			}
			t.Run(name, func(t *testing.T) {
				h, r := fairAssignmentHarness(t)
				draws := 0
				h.app.shuffleExecutors = func(n int, swap func(int, int)) {
					draws++
					if reverse {
						reverseExecutors(n, swap)
					}
				}
				var target string
				if role == "owner" {
					issue := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
					target = issue.IssueID
				} else {
					spec := testSpec
					spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
					_, task := h.createTask(owner, createTaskPayload{Title: "first selection", Spec: spec})
					target = task.TaskID
				}
				env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil)
				op := h.exec(runner("linux-1"), env, Claim{})
				var result claimResult
				if err := json.Unmarshal(op.Result, &result); err != nil {
					t.Fatal(err, op)
				}
				as := result.Assignment
				want := "alpha"
				if reverse {
					want = "beta"
				}
				if as.Execution == nil || as.Execution.Identity.LocalID != want || draws != 1 {
					t.Fatal("permutation did not determine first eligible choice", as, draws)
				}
				if role == "owner" && (as.Manager == nil || as.Manager.IssueID != target) || role == "worker" && as.TaskID != target {
					t.Fatal(as)
				}
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					pin, err := store.GetRunExecution(t.Context(), tx.Q(), as.RunID)
					if err == nil && *pin != *as.Execution {
						t.Fatal("assignment and stored pin differ", pin, as.Execution)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				h.app.shuffleExecutors = func(int, func(int, int)) { t.Fatal("replayed claim drew again") }
				retry := h.exec(runner("linux-1"), env, Claim{})
				var replay claimResult
				if err := json.Unmarshal(retry.Result, &replay); err != nil || replay.Assignment.RunID != as.RunID || replay.Assignment.LeaseID != as.LeaseID || replay.Assignment.Execution == nil || *replay.Assignment.Execution != *as.Execution {
					t.Fatal("replay changed execution", retry, err)
				}
				if h.count("runs") != 1 || h.count("leases") != 1 {
					t.Fatal("replay duplicated run or lease")
				}
			})
		}
	}
}

func TestFairAssignmentRetainsTaskRolesAcrossCompletionAndContinuation(t *testing.T) {
	h, r := fairAssignmentHarness(t)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "same sustained goal", Spec: spec})
	h.app.shuffleExecutors = reverseExecutors
	first, ok := h.claimInputRun(r)
	if !ok || first.Execution == nil || first.Execution.Identity.LocalID != "beta" {
		t.Fatal(first)
	}
	finishImplementation := func(as protocol.Assignment) {
		t.Helper()
		if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	finishReview := func(as protocol.Assignment) {
		t.Helper()
		if op := h.runAs(as, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		w := h.resourceForTask(task.TaskID)
		if !h.releaseResource(task.TaskID, w.WorkspaceID) {
			t.Fatal("workspace not physically released")
		}
	}
	finishImplementation(first)
	h.app.shuffleExecutors = func(int, func(int, int)) {}
	review, ok := h.claimInputRun(r)
	if !ok || review.Execution == nil || review.Execution.Identity.LocalID != "alpha" || review.Role != protocol.RoleAccept {
		t.Fatal("implementation pin constrained the new reviewer role", review)
	}
	finishReview(review)
	if op := continueTaskForTest(t, h, task.TaskID, "Check the remaining same-goal observation"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Once the completed Task is continued, hello must advertise both actual
	// role pins even though their producer Runs already ended. After removal,
	// only retained availability can make the same agreement runnable.
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	r.Sequence++
	hello := h.versionedHello("linux-1", r)
	var observed protocol.HelloResult
	if err := json.Unmarshal(hello.Result, &observed); err != nil || !slices.Contains(observed.RetainedExecutions, *first.Execution) || !slices.Contains(observed.RetainedExecutions, *review.Execution) {
		t.Fatal("continued Task lost actual role retention", hello, err)
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
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "removed-fresh-agents"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.AgentExecutors = nil
	for _, pin := range observed.RetainedExecutions {
		r.AgentContinuations = append(r.AgentContinuations, protocol.AgentExecutor{Available: true, Pin: pin})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(r)
	if !ok || next.Execution == nil || *next.Execution != *first.Execution || next.RunID == first.RunID {
		t.Fatal("Task continuation redrew implementation", next)
	}
	finishImplementation(next)
	h.app.shuffleExecutors = reverseExecutors
	nextReview, ok := h.claimInputRun(r)
	if !ok || nextReview.Execution == nil || *nextReview.Execution != *review.Execution {
		t.Fatal("Task continuation redrew reviewer", nextReview)
	}
	finishReview(nextReview)
	if h.count("tasks") != 1 || h.count("runs") != 4 || h.count("acceptances") != 2 {
		t.Fatal("continuation replaced history")
	}
}

func TestFairAssignmentAllowsConcurrentAgentUseButHonorsHostSlots(t *testing.T) {
	h, r := fairAssignmentHarness(t)
	h.app.shuffleExecutors = reverseExecutors
	for range 3 {
		spec := testSpec
		spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/alpha"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
		h.createTask(owner, createTaskPayload{Title: "explicit concurrent selection", Spec: spec})
	}
	for range 2 {
		as, ok := h.claimInputRun(r)
		if !ok || as.Execution == nil || as.Execution.Identity.LocalID != "alpha" {
			t.Fatal("explicit choice or concurrency was lost", as)
		}
	}
	if as, ok := h.claimInputRun(r); ok {
		t.Fatal("host slot gate was bypassed", as)
	}
}
