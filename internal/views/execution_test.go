package views

import (
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
)

func TestExecutionWaitOnlyCountsCapacityOnCompatibleAvailableHosts(t *testing.T) {
	candidate := scheduler.Candidate{Kind: protocol.ActionImplement, Status: protocol.ActionPending, Lifecycle: protocol.TaskQueued, Control: protocol.ControlManaged, Block: protocol.BlockNone, DependenciesDone: true, Requirements: domain.Requirements{OS: "linux"}}
	host := executionHost{Host: scheduler.Host{ID: "compatible", OS: "linux", Active: true, Slots: 1, Busy: 1}, available: true}
	f := executionFacts{hosts: []executionHost{host, {Host: scheduler.Host{ID: "other", OS: "windows", Active: true, Slots: 1, Busy: 1}, available: true}}}
	if reason := f.waitReason(candidate); reason.Code != protocol.QueueHostCapacity || reason.HostID != "compatible" {
		t.Fatal(reason)
	}
	f.hosts[0].available = false
	if reason := f.waitReason(candidate); reason.Code != protocol.QueueHostUnavailable || reason.HostID != "compatible" {
		t.Fatal(reason)
	}
	f.hosts[0].available = true
	f.hosts[0].Busy = 0
	if reason := f.waitReason(candidate); reason.Code != protocol.QueueReady {
		t.Fatal(reason)
	}
	// A retired workspace host cannot be substituted by unrelated capacity.
	candidate.WorkspaceHost = "retired"
	if reason := f.waitReason(candidate); reason.Code != protocol.QueueHostUnavailable || reason.HostID != "retired" {
		t.Fatal(reason)
	}
}

func TestExecutionCountsIgnorePlanStatusAndDeduplicateLinkedTasks(t *testing.T) {
	v := protocol.WorkItemView{WorkItem: protocol.WorkItem{WorkItemPlan: protocol.WorkItemPlan{Stages: []protocol.WorkItemStage{
		{StageID: "parent", Status: "active", TaskIDs: []string{"one"}},
		{StageID: "child", ParentID: "parent", Status: "active", TaskIDs: []string{"one", "two"}},
		{StageID: "empty", Status: "active"},
	}}}, Tasks: []protocol.TaskSummary{
		{TaskID: "one", Execution: &protocol.TaskExecutionView{State: protocol.ExecutionStarting, Role: protocol.RoleAccept}},
		{TaskID: "two", Execution: &protocol.TaskExecutionView{State: protocol.ExecutionQueued, Role: protocol.RoleAccept}},
	}}
	projectIssueExecution(&v)
	if v.Execution.Starting != 1 || v.Execution.Accepting != 0 || v.Execution.Queued != 1 || v.Execution.Running != 0 {
		t.Fatal(v.Execution)
	}
	if v.StageExecution[0].Execution.Starting != 1 || v.StageExecution[0].Execution.Accepting != 0 || v.StageExecution[0].Execution.Queued != 1 {
		t.Fatal(v.StageExecution)
	}
	if v.StageExecution[2].Execution != (protocol.ExecutionCounts{}) {
		t.Fatal("empty active stage fabricated execution")
	}
}
