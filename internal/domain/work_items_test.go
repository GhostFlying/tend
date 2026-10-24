package domain

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestWorkItemGraphAndHistoricalIdentity(t *testing.T) {
	base := protocol.WorkItemPlan{Title: "Delivery", Project: "default", Intent: "Investigate then deliver", Stages: []protocol.WorkItemStage{{StageID: "research", Title: "Research", Status: "planned"}, {StageID: "implement", Title: "Implement", Status: "planned", DependsOn: []string{"research"}}}}
	if err := ValidateWorkItem(base, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*protocol.WorkItemPlan)
	}{
		{"dependency cycle", func(p *protocol.WorkItemPlan) { p.Stages[0].DependsOn = []string{"implement"} }},
		{"parent dependency cycle", func(p *protocol.WorkItemPlan) { p.Stages[0].ParentID = "implement" }},
		{"missing dependency", func(p *protocol.WorkItemPlan) { p.Stages[0].DependsOn = []string{"unknown"} }},
		{"remove stage", func(p *protocol.WorkItemPlan) { p.Stages = p.Stages[:1] }},
		{"complete without evidence", func(p *protocol.WorkItemPlan) { p.Stages[0].Status = "done" }},
		{"retire without reason", func(p *protocol.WorkItemPlan) { p.Stages[0].Status = "retired" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := base
			p.Stages = append([]protocol.WorkItemStage(nil), base.Stages...)
			test.mutate(&p)
			if err := ValidateWorkItem(p, &base); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	p := base
	p.Stages = append([]protocol.WorkItemStage(nil), base.Stages...)
	p.Stages[0].Status = "retired"
	p.Stages[0].Reason = "Owner chose another approach"
	if err := ValidateWorkItem(p, &base); err != nil {
		t.Fatal(err)
	}
}
func TestWorkItemCompletionRequiresOwnerEndpoint(t *testing.T) {
	item := protocol.WorkItem{WorkItemPlan: protocol.WorkItemPlan{Title: "Research", Project: "default", Intent: "Determine next steps"}}
	evidence := protocol.WorkItemEvidence{Kind: "acceptance", Reference: "acceptance", Summary: "Investigation was accurate"}
	closure := protocol.WorkItemClosure{Kind: "completed", Reason: "Investigation complete", Evidence: []protocol.WorkItemEvidence{evidence}}
	if err := ValidateWorkItemClosure(item, closure); err == nil {
		t.Fatal("investigation acceptance inferred responsibility endpoint")
	}
	closure.Evidence = append(closure.Evidence, protocol.WorkItemEvidence{Kind: "owner_decision", Reference: "owner message", Summary: "Owner only requested this investigation"})
	if err := ValidateWorkItemClosure(item, closure); err != nil {
		t.Fatal(err)
	}
}
func TestWorkItemDeliveryIdentityCannotBeRebound(t *testing.T) {
	old := protocol.WorkItemPlan{Title: "Ship", Project: "default", Intent: "Ship change", Deliveries: []protocol.WorkItemDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 1}}}
	next := old
	next.Deliveries = append([]protocol.WorkItemDelivery(nil), old.Deliveries...)
	next.Deliveries[0].Number = 2
	if err := ValidateWorkItem(next, &old); err == nil {
		t.Fatal("old provider observation could overwrite replacement target")
	}
	next.Deliveries = append(append([]protocol.WorkItemDelivery(nil), old.Deliveries...), protocol.WorkItemDelivery{DeliveryID: "follow-up", Provider: "github", Repository: "example/repo", Number: 2})
	if err := ValidateWorkItem(next, &old); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemParentCompletionRequiresChildren(t *testing.T) {
	plan := protocol.WorkItemPlan{Title: "Grouped", Project: "default", Intent: "Finish grouped work", Stages: []protocol.WorkItemStage{{StageID: "group", Title: "Group", Status: "done", Evidence: []protocol.WorkItemEvidence{{Kind: "owner_decision", Reference: "owner", Summary: "confirmed"}}}, {StageID: "child", ParentID: "group", Title: "Child", Status: "planned"}}}
	if err := ValidateWorkItem(plan, nil); err == nil {
		t.Fatal("parent completion hid unfinished child")
	}
	plan.Stages[1].Status = "cancelled"
	plan.Stages[1].Reason = "Owner removed this branch"
	if err := ValidateWorkItem(plan, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemOpenProblemPreventsClosureButSuggestionDoesNot(t *testing.T) {
	closure := protocol.WorkItemClosure{Kind: "completed", Reason: "Owner endpoint", Evidence: []protocol.WorkItemEvidence{{Kind: "owner_decision", Reference: "owner", Summary: "Explicit responsibility endpoint"}}}
	item := protocol.WorkItem{WorkItemPlan: protocol.WorkItemPlan{FollowOns: []protocol.WorkItemFollowOn{{FollowOnID: "remaining", Kind: "problem", Text: "Known regression", Source: "test", Status: "open"}}}}
	if err := ValidateWorkItemClosure(item, closure); err == nil {
		t.Fatal("unresolved problem hidden")
	}
	item.FollowOns[0].Kind = "suggestion"
	if err := ValidateWorkItemClosure(item, closure); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemChildCannotDependOnContainingGroup(t *testing.T) {
	plan := protocol.WorkItemPlan{Title: "Group", Project: "default", Intent: "Execute nested work", Stages: []protocol.WorkItemStage{{StageID: "outer", Title: "Outer", Status: "planned"}, {StageID: "inner", ParentID: "outer", Title: "Inner", Status: "planned"}, {StageID: "child", ParentID: "inner", Title: "Child", Status: "planned", DependsOn: []string{"outer"}}}}
	if err := ValidateWorkItem(plan, nil); err == nil {
		t.Fatal("child waits for a group whose completion waits for child")
	}
}

func TestWorkItemRejectsTransitiveGroupCompletionCycles(t *testing.T) {
	for _, test := range []struct {
		name   string
		stages []protocol.WorkItemStage
	}{
		{"transitive containing group", []protocol.WorkItemStage{
			{StageID: "group"},
			{StageID: "child", ParentID: "group", DependsOn: []string{"before"}},
			{StageID: "before", DependsOn: []string{"group"}},
		}},
		{"dependency inherits ancestor prerequisite", []protocol.WorkItemStage{
			{StageID: "group"},
			{StageID: "child", ParentID: "group", DependsOn: []string{"before"}},
			{StageID: "before", ParentID: "other"},
			{StageID: "other", DependsOn: []string{"group"}},
		}},
		{"nested group inherits prerequisite", []protocol.WorkItemStage{
			{StageID: "group"},
			{StageID: "nested", ParentID: "group", DependsOn: []string{"before"}},
			{StageID: "child", ParentID: "nested"},
			{StageID: "before", DependsOn: []string{"group"}},
		}},
		{"cross group completion cycle", []protocol.WorkItemStage{
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
			plan := protocol.WorkItemPlan{Title: "Grouped", Project: "default", Intent: "Execute grouped work", Stages: test.stages}
			if err := ValidateWorkItem(plan, nil); err == nil {
				t.Fatal("accepted a group completion deadlock")
			}
		})
	}
}

func TestWorkItemAllowsCrossGroupParallelPrerequisites(t *testing.T) {
	plan := protocol.WorkItemPlan{Title: "Grouped", Project: "default", Intent: "Execute grouped work", Stages: []protocol.WorkItemStage{
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
	if err := ValidateWorkItem(plan, nil); err != nil {
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
			w.Stages = append(w.Stages, protocol.WorkItemStage{StageID: "child", ParentID: "first", TaskIDs: []string{"one"}, Status: "planned"})
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := protocol.WorkItem{WorkItemID: "item", Status: "open", WorkItemPlan: protocol.WorkItemPlan{Stages: []protocol.WorkItemStage{
				{StageID: "first", Status: "planned", TaskIDs: []string{"one"}},
				{StageID: "middle", Status: "planned", DependsOn: []string{"first"}, TaskIDs: []string{"two"}},
				{StageID: "last", Status: "planned", DependsOn: []string{"middle"}, TaskIDs: []string{"one"}},
			}}}
			states := map[string]protocol.TaskLifecycle{"one": protocol.TaskQueued, "two": protocol.TaskQueued}
			test.mutate(&w, states)
			err := ValidateWorkItemTaskDependencies([]protocol.WorkItem{w}, states, nil)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}
