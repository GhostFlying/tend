package scheduler

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerKeepsOriginalSnapshotAcrossUnrelatedInventoryRevision(t *testing.T) {
	pin := protocol.RunExecutionPin{Purpose: "issue_manager", Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner", LocalID: "manager"}, AgentRevision: 1, SnapshotID: "original", InventoryRevision: 1, PublicSHA256: "old-hash", Profile: protocol.ExecutionProfile{Model: "model"}}
	observed := pin
	observed.SnapshotID = "updated-inventory"
	observed.InventoryRevision++
	observed.PublicSHA256 = "new-hash"
	host := Host{ID: "runner", Executors: []Executor{{Pin: observed, Categories: []string{"issue-manager"}}}}
	candidate := Candidate{Manager: &protocol.IssueManagerSpec{CategoryID: "issue-manager"}, ManagerPin: &pin}
	got, reason := SelectExecution(host, candidate)
	if reason != "" || got == nil || *got != pin {
		t.Fatal("inventory receipt replaced a continuing manager's frozen snapshot", got, reason)
	}
	host.Executors[0].Pin.AgentRevision++
	if got, reason := SelectExecution(host, candidate); got != nil || reason == "" {
		t.Fatal("a changed definition replaced the original manager", got, reason)
	}
}

func TestContinuationKeepsBackendAndIndependentAcceptanceAdmission(t *testing.T) {
	old := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner", LocalID: "cedar"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "native", Revision: 1}, InventoryRevision: 1, SnapshotID: "original", PublicSHA256: "old", Profile: protocol.ExecutionProfile{Model: "model"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
	current := old
	current.AgentRevision = 2
	current.SnapshotID = "current"
	review := current
	review.Identity.LocalID = "maple"
	h := Host{ID: "runner", IssueOwners: true, ContinuationsSupported: true, Executors: []Executor{{Pin: current, Categories: []string{"implementation"}}}, Continuations: []Executor{{Continuation: true, Pin: old, Categories: []string{"implementation"}}}}
	c := Candidate{Kind: protocol.ActionImplement, OwnerAgentRevision: 1, OwnerRuntimeRevision: 1, OwnerPin: &old, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementation"}, Acceptance: protocol.AgentRequirement{CategoryID: "review"}}}
	if pin, _ := SelectExecution(h, c); pin != nil {
		t.Fatal("retention bypassed first-allocation acceptance admission", pin)
	}
	h.Executors = append(h.Executors, Executor{Pin: review, Categories: []string{"review"}})
	if pin, reason := SelectExecution(h, c); pin == nil || *pin != old || reason != "" {
		t.Fatal("original owner was not selected", pin, reason)
	}
	c.WorkspaceBackendID = "orca"
	if pin, _ := SelectExecution(h, c); pin != nil {
		t.Fatal("retention bypassed workspace backend", pin)
	}
	c.WorkspaceBackendID = ""
	c.OwnerPin = nil
	c.OwnerAgentRevision = 0
	c.OwnerRuntimeRevision = 0
	if pin, _ := SelectExecution(h, c); pin == nil || *pin != current {
		t.Fatal("new allocation used a historical candidate", pin)
	}
	h.Executors = nil
	if pin, _ := SelectExecution(h, c); pin != nil {
		t.Fatal("unbound work selected a continuation", pin)
	}
}

func TestManagersRespectConfiguredCapacity(t *testing.T) {
	pin := protocol.RunExecutionPin{Purpose: "issue_manager", Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner", LocalID: "manager"}, AgentRevision: 1}
	c := implement()
	c.Kind = protocol.ActionCoordinate
	c.Manager = &protocol.IssueManagerSpec{IssueID: "issue", CategoryID: "issue-manager"}
	c.ManagerPin = &pin
	for _, slots := range []int64{0, 1} {
		h := Host{ID: "runner", Active: true, Slots: slots, Busy: slots, IssueManagers: true}
		if reason := SkipReason(h, c); reason == nil || reason.Code != protocol.QueueHostCapacity {
			t.Fatal("manager exceeded configured capacity", slots, reason)
		}
	}
}
