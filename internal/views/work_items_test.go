package views

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestWorkItemBoardDoesNotConfuseExecutionWithClosure(t *testing.T) {
	v := protocol.WorkItemView{WorkItem: protocol.WorkItem{Status: "open"}, Tasks: []protocol.TaskSummary{{Lifecycle: protocol.TaskCompleted}}}
	if got := WorkItemColumn(v); got != "unplanned" {
		t.Fatal(got)
	}
	v.Tasks = append(v.Tasks, protocol.TaskSummary{Lifecycle: protocol.TaskAwaitingAcceptance, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone})
	if got := WorkItemColumn(v); got != "checking" {
		t.Fatal(got)
	}
	v.Tasks = append(v.Tasks, protocol.TaskSummary{Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone})
	if got := WorkItemColumn(v); got != "active" {
		t.Fatal(got)
	}
	v.FollowOns = []protocol.WorkItemFollowOn{{Kind: "problem", Status: "open"}}
	if got := WorkItemColumn(v); got != "attention" {
		t.Fatal(got)
	}
	v.Status = "snoozed"
	if got := WorkItemColumn(v); got != "snoozed" {
		t.Fatal(got)
	}
}
