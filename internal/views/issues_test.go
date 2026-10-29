package views

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestWorkItemBoardDoesNotConfuseExecutionWithClosure(t *testing.T) {
	v := protocol.WorkItemView{WorkItem: protocol.WorkItem{Status: "open"}, Tasks: []protocol.TaskSummary{{Lifecycle: protocol.TaskCompleted}}}
	if got := IssueColumn(v); got != "unplanned" {
		t.Fatal(got)
	}
	v.Tasks = append(v.Tasks, protocol.TaskSummary{Lifecycle: protocol.TaskAwaitingAcceptance, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone})
	if got := IssueColumn(v); got != "checking" {
		t.Fatal(got)
	}
	v.Tasks = append(v.Tasks, protocol.TaskSummary{Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone})
	if got := IssueColumn(v); got != "active" {
		t.Fatal(got)
	}
	v.Tasks = []protocol.TaskSummary{{Lifecycle: protocol.TaskCancelled, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone, Execution: &protocol.TaskExecutionView{State: protocol.ExecutionUnknown, RunID: "run"}}}
	if got := IssueColumn(v); got != "active" {
		t.Fatalf("cancelled task with unresolved run = %s", got)
	}
	v.Tasks[0].Execution = &protocol.TaskExecutionView{State: protocol.ExecutionCancelled}
	if got := IssueColumn(v); got != "unplanned" {
		t.Fatalf("settled cancelled task = %s", got)
	}
	v.FollowOns = []protocol.IssueFollowOn{{Kind: "problem", Status: "open"}}
	if got := IssueColumn(v); got != "attention" {
		t.Fatal(got)
	}
	v.Status = "snoozed"
	if got := IssueColumn(v); got != "snoozed" {
		t.Fatal(got)
	}
}
