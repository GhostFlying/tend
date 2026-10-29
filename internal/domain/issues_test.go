package domain

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestWorkItemGraphAndHistoricalIdentity(t *testing.T) {
	base := protocol.IssuePlan{Title: "Delivery", Project: "default", Intent: "Investigate then deliver", Stages: []protocol.IssueStage{{StageID: "research", Title: "Research", Status: "planned"}, {StageID: "implement", Title: "Implement", Status: "planned", DependsOn: []string{"research"}}}}
	if err := ValidateIssue(base, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*protocol.IssuePlan)
	}{
		{"dependency cycle", func(p *protocol.IssuePlan) { p.Stages[0].DependsOn = []string{"implement"} }},
		{"parent dependency cycle", func(p *protocol.IssuePlan) { p.Stages[0].ParentID = "implement" }},
		{"missing dependency", func(p *protocol.IssuePlan) { p.Stages[0].DependsOn = []string{"unknown"} }},
		{"remove stage", func(p *protocol.IssuePlan) { p.Stages = p.Stages[:1] }},
		{"complete without evidence", func(p *protocol.IssuePlan) { p.Stages[0].Status = "done" }},
		{"retire without reason", func(p *protocol.IssuePlan) { p.Stages[0].Status = "retired" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := base
			p.Stages = append([]protocol.IssueStage(nil), base.Stages...)
			test.mutate(&p)
			if err := ValidateIssue(p, &base); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	p := base
	p.Stages = append([]protocol.IssueStage(nil), base.Stages...)
	p.Stages[0].Status = "retired"
	p.Stages[0].Reason = "Owner chose another approach"
	if err := ValidateIssue(p, &base); err != nil {
		t.Fatal(err)
	}
}
func TestWorkItemCompletionRequiresOwnerEndpoint(t *testing.T) {
	item := protocol.WorkItem{IssuePlan: protocol.IssuePlan{Title: "Research", Project: "default", Intent: "Determine next steps"}}
	evidence := protocol.IssueEvidence{Kind: "acceptance", Reference: "acceptance", Summary: "Investigation was accurate"}
	closure := protocol.IssueClosure{Kind: "completed", Reason: "Investigation complete", Evidence: []protocol.IssueEvidence{evidence}}
	if err := ValidateIssueClosure(item, closure); err == nil {
		t.Fatal("investigation acceptance inferred responsibility endpoint")
	}
	closure.Evidence = append(closure.Evidence, protocol.IssueEvidence{Kind: "owner_decision", Reference: "owner message", Summary: "Owner only requested this investigation"})
	if err := ValidateIssueClosure(item, closure); err != nil {
		t.Fatal(err)
	}
}
func TestWorkItemDeliveryIdentityCannotBeRebound(t *testing.T) {
	old := protocol.IssuePlan{Title: "Ship", Project: "default", Intent: "Ship change", Deliveries: []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 1}}}
	next := old
	next.Deliveries = append([]protocol.IssueDelivery(nil), old.Deliveries...)
	next.Deliveries[0].Number = 2
	if err := ValidateIssue(next, &old); err == nil {
		t.Fatal("old provider observation could overwrite replacement target")
	}
	next.Deliveries = append(append([]protocol.IssueDelivery(nil), old.Deliveries...), protocol.IssueDelivery{DeliveryID: "follow-up", Provider: "github", Repository: "example/repo", Number: 2})
	if err := ValidateIssue(next, &old); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemParentCompletionRequiresChildren(t *testing.T) {
	plan := protocol.IssuePlan{Title: "Grouped", Project: "default", Intent: "Finish grouped work", Stages: []protocol.IssueStage{{StageID: "group", Title: "Group", Status: "done", Evidence: []protocol.IssueEvidence{{Kind: "owner_decision", Reference: "owner", Summary: "confirmed"}}}, {StageID: "child", ParentID: "group", Title: "Child", Status: "planned"}}}
	if err := ValidateIssue(plan, nil); err == nil {
		t.Fatal("parent completion hid unfinished child")
	}
	plan.Stages[1].Status = "cancelled"
	plan.Stages[1].Reason = "Owner removed this branch"
	if err := ValidateIssue(plan, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemOpenProblemPreventsClosureButSuggestionDoesNot(t *testing.T) {
	closure := protocol.IssueClosure{Kind: "completed", Reason: "Owner endpoint", Evidence: []protocol.IssueEvidence{{Kind: "owner_decision", Reference: "owner", Summary: "Explicit responsibility endpoint"}}}
	item := protocol.WorkItem{IssuePlan: protocol.IssuePlan{FollowOns: []protocol.IssueFollowOn{{FollowOnID: "remaining", Kind: "problem", Text: "Known regression", Source: "test", Status: "open"}}}}
	if err := ValidateIssueClosure(item, closure); err == nil {
		t.Fatal("unresolved problem hidden")
	}
	item.FollowOns[0].Kind = "suggestion"
	if err := ValidateIssueClosure(item, closure); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemChildCannotDependOnContainingGroup(t *testing.T) {
	plan := protocol.IssuePlan{Title: "Group", Project: "default", Intent: "Execute nested work", Stages: []protocol.IssueStage{{StageID: "outer", Title: "Outer", Status: "planned"}, {StageID: "inner", ParentID: "outer", Title: "Inner", Status: "planned"}, {StageID: "child", ParentID: "inner", Title: "Child", Status: "planned", DependsOn: []string{"outer"}}}}
	if err := ValidateIssue(plan, nil); err == nil {
		t.Fatal("child waits for a group whose completion waits for child")
	}
}

func TestWorkItemRejectsTransitiveGroupCompletionCycles(t *testing.T) {
	for _, test := range []struct {
		name   string
		stages []protocol.IssueStage
	}{
		{"transitive containing group", []protocol.IssueStage{
			{StageID: "group"},
			{StageID: "child", ParentID: "group", DependsOn: []string{"before"}},
			{StageID: "before", DependsOn: []string{"group"}},
		}},
		{"dependency inherits ancestor prerequisite", []protocol.IssueStage{
			{StageID: "group"},
			{StageID: "child", ParentID: "group", DependsOn: []string{"before"}},
			{StageID: "before", ParentID: "other"},
			{StageID: "other", DependsOn: []string{"group"}},
		}},
		{"nested group inherits prerequisite", []protocol.IssueStage{
			{StageID: "group"},
			{StageID: "nested", ParentID: "group", DependsOn: []string{"before"}},
			{StageID: "child", ParentID: "nested"},
			{StageID: "before", DependsOn: []string{"group"}},
		}},
		{"cross group completion cycle", []protocol.IssueStage{
			{StageID: "left"},
			{StageID: "left-child", ParentID: "left", DependsOn: []string{"right"}},
			{StageID: "right"},
			{StageID: "right-child", ParentID: "right", DependsOn: []string{"left"}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for i := range test.stages {
				test.stages[i].Title = test.stages[i].StageID
				test.stages[i].Status = "planned"
			}
			plan := protocol.IssuePlan{Title: "Grouped", Project: "default", Intent: "Execute grouped work", Stages: test.stages}
			if err := ValidateIssue(plan, nil); err == nil {
				t.Fatal("accepted a group completion deadlock")
			}
		})
	}
}

func TestWorkItemAllowsCrossGroupParallelPrerequisites(t *testing.T) {
	plan := protocol.IssuePlan{Title: "Grouped", Project: "default", Intent: "Execute grouped work", Stages: []protocol.IssueStage{
		{StageID: "setup"},
		{StageID: "left", DependsOn: []string{"setup"}},
		{StageID: "left-first", ParentID: "left"},
		{StageID: "left-second", ParentID: "left", DependsOn: []string{"right-first"}},
		{StageID: "right", DependsOn: []string{"setup"}},
		{StageID: "right-first", ParentID: "right"},
		{StageID: "right-second", ParentID: "right", DependsOn: []string{"left-first"}},
		{StageID: "publish", DependsOn: []string{"left", "right"}},
	}}
	for i := range plan.Stages {
		plan.Stages[i].Title = plan.Stages[i].StageID
		plan.Stages[i].Status = "planned"
	}
	if err := ValidateIssue(plan, nil); err != nil {
		t.Fatalf("independent cross-group branches are executable: %v", err)
	}
}

func TestWorkItemTaskDependencyGraph(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*protocol.WorkItem, map[string]protocol.TaskLifecycle)
		valid  bool
	}{
		{"non-contiguous coverage cycle", func(*protocol.WorkItem, map[string]protocol.TaskLifecycle) {}, false},
		{"continuous same task", func(w *protocol.WorkItem, _ map[string]protocol.TaskLifecycle) { w.Stages[1].TaskIDs = []string{"one"} }, true},
		{"completed historical task", func(_ *protocol.WorkItem, states map[string]protocol.TaskLifecycle) {
			states["one"] = protocol.TaskCompleted
		}, true},
		{"cancelled historical task", func(_ *protocol.WorkItem, states map[string]protocol.TaskLifecycle) {
			states["one"] = protocol.TaskCancelled
		}, true},
		{"done prerequisite", func(w *protocol.WorkItem, _ map[string]protocol.TaskLifecycle) { w.Stages[0].Status = "done" }, true},
		{"group completion waits for child task", func(w *protocol.WorkItem, _ map[string]protocol.TaskLifecycle) {
			w.Stages[0].TaskIDs = nil
			w.Stages = append(w.Stages, protocol.IssueStage{StageID: "child", ParentID: "first", TaskIDs: []string{"one"}, Status: "planned"})
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := protocol.WorkItem{WorkItemID: "item", Status: "open", IssuePlan: protocol.IssuePlan{Stages: []protocol.IssueStage{
				{StageID: "first", Status: "planned", TaskIDs: []string{"one"}},
				{StageID: "middle", Status: "planned", DependsOn: []string{"first"}, TaskIDs: []string{"two"}},
				{StageID: "last", Status: "planned", DependsOn: []string{"middle"}, TaskIDs: []string{"one"}},
			}}}
			states := map[string]protocol.TaskLifecycle{"one": protocol.TaskQueued, "two": protocol.TaskQueued}
			test.mutate(&w, states)
			err := ValidateIssueTaskDependencies([]protocol.WorkItem{w}, states, nil)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}
