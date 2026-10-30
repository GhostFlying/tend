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
