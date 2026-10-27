package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkItemToolsKeepUndecidedConcernVisibleAndRejectStalePlans(t *testing.T) {
	e := newEnv(t)
	plan := protocol.WorkItemPlan{Title: "Investigate layout", Intent: "Explain the overlapping controls", Stages: []protocol.WorkItemStage{{StageID: "inspect", Title: "Investigate", Status: "planned"}}, Background: []protocol.SharedFact{{Kind: "observation", Text: "Controls overlap on a short screen", Source: "owner screenshot"}}}
	created := e.call("work_items_create", workItemCreateArgs{InitialContext: initialFixtureContext(), Plan: plan, CommitSeq: 1, Reason: "Track the investigation and later discussion"}, "create-work")
	if !created.OK {
		t.Fatal(created.Error)
	}
	var result struct{ Outcome protocol.WorkItem }
	if err := json.Unmarshal(created.Result, &result); err != nil {
		t.Fatal(err)
	}
	w := result.Outcome
	if w.WorkItemID == "" || w.Status != "open" {
		t.Fatalf("unexpected item: %+v", w)
	}
	listed := e.call("work_items_list", workItemsListArgs{}, "list-work")
	if !listed.OK || !strings.Contains(string(listed.Result), w.WorkItemID) {
		t.Fatalf("undecided concern hidden: %s %+v", listed.Result, listed.Error)
	}
	plan = w.WorkItemPlan
	plan.Stages = append(plan.Stages, protocol.WorkItemStage{StageID: "check", Title: "Validate on two platforms", DependsOn: []string{"inspect"}, Status: "planned"})
	args := workItemUpdateArgs{WorkItemID: w.WorkItemID, ExpectedControlRevision: 1, Plan: plan, CommitSeq: 2, Reason: "Refine only the plan"}
	updated := e.call("work_items_update", args, "update-work")
	if !updated.OK {
		t.Fatal(updated.Error)
	}
	args.CommitSeq = 3
	stale := e.call("work_items_update", args, "stale-work")
	if stale.OK || stale.Error.Code != protocol.CodeStaleRevision || !strings.Contains(string(stale.Current), "Validate on two platforms") {
		t.Fatalf("stale write lacks current plan: %+v %s", stale.Error, stale.Current)
	}
	historical := e.call("work_items_read", workItemReadArgs{WorkItemID: w.WorkItemID, Revision: 1}, "history-work")
	if !historical.OK || strings.Contains(string(historical.Result), "Validate on two platforms") {
		t.Fatalf("history overwritten: %s %+v", historical.Result, historical.Error)
	}
}

func initialFixtureContext() protocol.InitialWorkItemContext {
	return protocol.InitialWorkItemContext{Coverage: "Reviewed fixture input", Gaps: "None in fixture", ReviewedKinds: []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}}
}
