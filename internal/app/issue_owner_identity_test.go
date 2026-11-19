package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func ownerIdentityRunner(t *testing.T, h *harness, host string, index int) *protocol.RunnerRuntime {
	t.Helper()
	runtime := testRuntime("issue-owner-" + host)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentInventory, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityIssueOwner, protocol.CapabilityPauseHandoffs)
	runtime.JournalVersion = 13
	if op := h.versionedHello(host, runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	inventory, _ := inventoryProjection(t, host)
	inventory.FormatVersion = 2
	inventory.Runtimes[0].CLI = "codex"
	agent := inventory.Agents[index]
	agent.Categories = []string{"implementer"}
	inventory.Agents = []protocol.AgentInventoryDefinition{agent}
	_, hash, err := protocol.CanonicalAgentInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	const snapshot = "issue-owner-inventory"
	env := h.env(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: host}, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: snapshot, PublicSHA256: hash, Projection: inventory}, rev(0))
	if op := h.exec(runner(host), env, RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{
		Purpose: "issue_manager", Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding,
		InventoryRevision: 1, SnapshotID: snapshot, PublicSHA256: hash,
		Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer,
	}}}
	runtime.Sequence++
	if op := h.versionedHello(host, runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return runtime
}

func TestIssueOwnerIdentityAcrossReopenAndPause(t *testing.T) {
	for _, path := range []string{"reopen command", "board and historical category"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			cfg := testConfig()
			cfg.Hosts = append(cfg.Hosts, HostConfig{HostID: "linux-2", OS: "linux", Arch: "amd64", Slots: 1, Repos: []string{"tend"}, TokenHash: strings.Repeat("cd", 32), Nodes: []string{"tag:tend-runner"}})
			if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			inventoryCategory(t, h, "implementer", 0, "active")
			alpha := ownerIdentityRunner(t, h, "linux-1", 0)
			beta := ownerIdentityRunner(t, h, "linux-2", 1)
			claim := func(host string, runtime *protocol.RunnerRuntime) *protocol.Assignment {
				t.Helper()
				env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: host}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil)
				op := h.exec(runner(host), env, Claim{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				var result struct {
					Assignment *protocol.Assignment `json:"assignment"`
				}
				if err := json.Unmarshal(op.Result, &result); err != nil {
					t.Fatal(err)
				}
				return result.Assignment
			}
			available := func(runtime *protocol.RunnerRuntime, host string, open bool) {
				t.Helper()
				runtime.AgentExecutors[0].Available = open
				runtime.Sequence++
				if op := h.versionedHello(host, runtime); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			choice := &protocol.IssueOwnerRequest{CategoryID: "implementer"}
			i := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, choice)
			if i.Assignee.Kind == protocol.IssueAssigneeAgent || i.OwnerSelection.AgentID != "" {
				t.Fatal("the first admission must be an unclaimed category", i.IssueControl)
			}
			first := claim("linux-1", alpha)
			if first == nil || first.Manager.IssueID != i.IssueID || first.Execution.Identity != alpha.AgentExecutors[0].Pin.Identity {
				t.Fatal("first claim did not take its category responsibility", first)
			}
			i = admissionCurrent(t, h, i.IssueID)
			const original = "linux-1/alpha"
			if i.Assignee.Ref != original {
				t.Fatal(i.IssueControl)
			}
			if path == "board and historical category" {
				// The older server wrote the assignee but left the original
				// category-only selection. Exercise that durable input.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					previous := i.Revision
					i.OwnerSelection.AgentID = ""
					i.Revision++
					return tx.SaveCanonicalIssue(t.Context(), i, previous, store.IssueRevisionProvenance{AdmissionDefinition: true, Actor: owner, RequestID: "test-historical-issue-admission"})
				}); err != nil {
					t.Fatal(err)
				}
			}
			stopOwnerClaim(t, h, *first)
			closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
			if path == "reopen command" {
				i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, closed, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: closed.AssignmentEpoch}))
				i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, choice)
			} else {
				i = issueAdmissionStatus(t, h, closed, protocol.IssueBacklog, nil)
				i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
			}
			if i.Assignee.Ref != original {
				t.Fatal("reopening changed responsibility", i.IssueControl)
			}
			available(alpha, "linux-1", false)
			if next := claim("linux-2", beta); next != nil {
				t.Fatal("another Agent in the same category claimed a reopened Issue", next)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				view, err := views.CanonicalIssue(t.Context(), tx.Q(), i.IssueID, 0)
				if err == nil && (view.QueueReason == nil || view.QueueReason.HostID != "linux-1" || !strings.Contains(view.QueueReason.Message, original) || view.QueueReason.Code == protocol.QueueReady) {
					t.Fatal("missing wait reason for the original Agent", view.QueueReason)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if i.AssignmentEpoch != closed.AssignmentEpoch {
				t.Fatal("reopening fenced the original responsibility as if it had been replaced", i.IssueControl)
			}
			available(alpha, "linux-1", true)
			reopened := claim("linux-1", alpha)
			if reopened == nil || reopened.Execution.Identity != first.Execution.Identity || reopened.TaskID == first.TaskID || h.task(first.TaskID).Lifecycle != protocol.TaskCompleted || h.run(first.RunID).ObservedState != protocol.RunFailed || h.lease(first.RunID).Status != store.LeaseReleased {
				t.Fatal("reopen did not preserve owner identity and closed Task history", reopened)
			}
			i = admissionCurrent(t, h, i.IssueID)
			if i.Assignee.Ref != original || i.OwnerSelection.AgentID != original {
				t.Fatal(i.IssueControl)
			}

			i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: i.AssignmentEpoch}))
			paused := h.run(reopened.RunID)
			if paused.PauseHandoff == nil {
				t.Fatal("pause lacks a physical handoff", paused)
			}
			if op := h.runAs(*reopened, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, YieldPause{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "resume", canonicalOwnerControl{ExpectedAssignmentEpoch: i.AssignmentEpoch}))
			available(alpha, "linux-1", false)
			if next := claim("linux-2", beta); next != nil {
				t.Fatal("another Agent in the same category claimed a resumed Issue", next)
			}
			available(alpha, "linux-1", true)
			resumed := claim("linux-1", alpha)
			current := admissionCurrent(t, h, i.IssueID)
			if resumed == nil || resumed.TaskID != reopened.TaskID || resumed.RunID == reopened.RunID || *resumed.Execution != *reopened.Execution ||
				current.Assignee.Ref != original || current.AssignmentEpoch != i.AssignmentEpoch || h.lease(reopened.RunID).Status != store.LeaseReleased {
				t.Fatal("resume did not retain the Task, assignment and execution pin", resumed, current.IssueControl)
			}

			const replacement = "linux-2/beta"
			target := protocol.Target{Type: "issue", ID: i.IssueID}
			op := h.exec(owner, h.env("issues.set_status", target, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Status: protocol.IssueTodo, Owner: &protocol.IssueOwnerRequest{CategoryID: choice.CategoryID, AgentID: replacement}}, rev(current.Revision)), CanonicalOwnerIssueCommand{Action: "set_status"})
			expectWorkCode(t, op, protocol.CodeInvalidTransition)
			request := protocol.RequestIssueManagerPayload{CategoryID: choice.CategoryID, AgentID: replacement, ExpectedAssignmentEpoch: current.AssignmentEpoch, Replace: true, Reason: "Explicitly hand the continuing Issue to the other named Agent"}
			env := h.env(protocol.OpIssueRequestOwner, target, request, rev(current.Revision))
			if _, err := h.app.Execute(t.Context(), runner("linux-2"), env, RequestIssueOwner{}); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("runner could authorize its own handoff: %v", err)
			}
			fenced := canonicalLifecycleIssue(t, h.exec(owner, env, RequestIssueOwner{}))
			routine := protocol.RequestIssueManagerPayload{CategoryID: choice.CategoryID, ExpectedAssignmentEpoch: fenced.AssignmentEpoch, Reason: "Reaffirm the existing ownership choice"}
			reaffirmed := canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestOwner, target, routine, rev(fenced.Revision)), RequestIssueOwner{}))
			if next := claim("linux-2", beta); next != nil || fenced.Assignee.Ref != original {
				t.Fatal("new Agent claimed before old execution stopped", next, fenced.IssueControl)
			}
			stopOwnerClaim(t, h, *resumed)
			if next := claim("linux-1", alpha); next != nil {
				t.Fatal("original Agent claimed after explicit replacement", next)
			}
			transferred := claim("linux-2", beta)
			last := admissionCurrent(t, h, i.IssueID)
			if transferred == nil || transferred.Execution.Identity != beta.AgentExecutors[0].Pin.Identity || last.Assignee.Ref != replacement ||
				h.task(first.TaskID).Lifecycle != protocol.TaskCompleted || h.task(resumed.TaskID).Lifecycle != protocol.TaskCompleted {
				t.Fatal("explicit handoff failed or changed old Task history", transferred, last.IssueControl)
			}
			if reaffirmed.Revision != fenced.Revision || *reaffirmed.OwnerSelection != *fenced.OwnerSelection {
				t.Fatal("routine selection edited the pending handoff", reaffirmed.IssueControl)
			}
		})
	}
}
