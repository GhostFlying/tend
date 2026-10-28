package domain

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssuePrimaryTaskWaitsSeparateParentClosureAndDispatch(t *testing.T) {
	parent := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "parent", State: protocol.IssueActive}}
	child := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "child", ParentIssueID: "parent", State: protocol.IssueActive}}
	primary := map[string][]string{"parent": {"pt"}, "child": {"ct"}}
	lives := map[string]protocol.TaskLifecycle{"pt": protocol.TaskDraft, "ct": protocol.TaskDraft}
	waits := map[string][]string{"ct": {"pt"}}
	if err := ValidateIssuePrimaryTaskWaits([]protocol.Issue{parent, child}, primary, lives, waits); err != nil {
		t.Fatal("parent closure was mistaken for task dispatch", err)
	}
	parent.DependsOn = []string{"child"}
	if err := ValidateIssuePrimaryTaskWaits([]protocol.Issue{parent, child}, primary, lives, waits); err == nil {
		t.Fatal("explicit parent prerequisite plus task order cycle accepted")
	}
	child.State = protocol.IssueClosed
	child.Closure = &protocol.WorkItemClosure{Kind: protocol.WorkItemClosureCompleted}
	lives["ct"] = protocol.TaskCompleted
	if err := ValidateIssuePrimaryTaskWaits([]protocol.Issue{parent, child}, primary, lives, waits); err != nil {
		t.Fatal("achieved dependency was kept as a wait", err)
	}
}

func TestIssuePrimaryTaskWaitsIncludeExplicitStageWaits(t *testing.T) {
	issue := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "issue", State: protocol.IssueActive}, Plan: protocol.WorkItemPlan{Stages: []protocol.WorkItemStage{
		{StageID: "first", Status: protocol.WorkItemStagePlanned, TaskIDs: []string{"first"}},
		{StageID: "second", Status: protocol.WorkItemStagePlanned, DependsOn: []string{"first"}, TaskIDs: []string{"second"}},
	}}}
	primary := map[string][]string{"issue": {"first", "second"}}
	lives := map[string]protocol.TaskLifecycle{"first": protocol.TaskDraft, "second": protocol.TaskDraft}
	if err := ValidateIssuePrimaryTaskWaits([]protocol.Issue{issue}, primary, lives, nil); err != nil {
		t.Fatal("ordinary stage ordering rejected", err)
	}
	if err := ValidateIssuePrimaryTaskWaits([]protocol.Issue{issue}, primary, lives, map[string][]string{"first": {"second"}}); err == nil {
		t.Fatal("actual stage/task cycle accepted")
	}
}
