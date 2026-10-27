package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Advertise support in the isolated fixture without starting a runner.
func newPlanningEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	ids := testkit.NewIDGen(testkit.NewClock(), 17)
	payload, err := json.Marshal(protocol.HelloPayload{WorkspaceProtocol: 1})
	if err != nil {
		t.Fatal(err)
	}
	op, err := e.svc.app.Execute(context.Background(), protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, &protocol.Envelope{
		RequestID: ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerHello,
		Target: protocol.Target{Type: "host", ID: "linux-1"}, Payload: payload,
	}, app.Hello{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("advertise fixture capabilities: %+v (%v)", op, err)
	}
	return e
}

// Follow local references and nullable array wrappers exactly where a consumer
// reads a field, rather than checking an unused definition in isolation.
func planningSchemaAt(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	node := root
	resolve := func() {
		for {
			if ref, ok := node["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/definitions/") {
					t.Fatalf("unexpected reference %q", ref)
				}
				defs := root["definitions"].(map[string]any)
				node, ok = defs[strings.TrimPrefix(ref, "#/definitions/")].(map[string]any)
				if !ok {
					t.Fatalf("unresolved reference %q", ref)
				}
				continue
			}
			if alternatives, ok := node["anyOf"].([]any); ok {
				for _, alternative := range alternatives {
					candidate := alternative.(map[string]any)
					if candidate["type"] != "null" {
						node = candidate
						break
					}
				}
				continue
			}
			break
		}
	}
	for _, key := range path {
		resolve()
		if key == "[]" {
			node = node["items"].(map[string]any)
		} else {
			node = node["properties"].(map[string]any)[key].(map[string]any)
		}
	}
	resolve()
	return node
}

func TestPlanningSchemasExposeEnumsOnBothToolSurfaces(t *testing.T) {
	surfaces := map[string]map[string]json.RawMessage{"hosted": {}, "addon": {}}
	for _, spec := range Specs() {
		surfaces["hosted"][spec.Name] = spec.InputSchema
	}
	for _, spec := range AddonSpecs() {
		surfaces["addon"][spec.Name] = spec.InputSchema
	}
	cases := []struct {
		tool string
		path []string
		want any
	}{
		{"work_items_create", []string{"plan", "background", "[]", "kind"}, protocol.SharedFactKindValues()},
		{"work_items_create", []string{"plan", "stages", "[]", "status"}, protocol.WorkItemStageStatusValues()},
		{"work_items_update", []string{"plan", "stages", "[]", "status"}, protocol.WorkItemStageStatusValues()},
		{"work_items_update", []string{"status"}, protocol.WorkItemUpdateStatusValues()},
		{"work_items_update", []string{"task_dispositions", "[]", "action"}, protocol.WorkItemTaskDispositionActionValues()},
		{"work_items_create", []string{"plan", "follow_ons", "[]", "kind"}, protocol.WorkItemFollowOnKindValues()},
		{"work_items_create", []string{"plan", "follow_ons", "[]", "status"}, protocol.WorkItemFollowOnStatusValues()},
		{"work_items_create", []string{"plan", "deliveries", "[]", "provider"}, protocol.WorkItemDeliveryProviderValues()},
		{"work_items_close", []string{"closure", "kind"}, protocol.WorkItemClosureKindValues()},
		{"work_items_close", []string{"closure", "evidence", "[]", "kind"}, protocol.WorkItemEvidenceKindValues()},
	}
	for surface, specs := range surfaces {
		for _, tc := range cases {
			t.Run(surface+"/"+tc.tool+"/"+strings.Join(tc.path, "."), func(t *testing.T) {
				var root map[string]any
				if err := json.Unmarshal(specs[tc.tool], &root); err != nil {
					t.Fatal(err)
				}
				actual := planningSchemaAt(t, root, tc.path...)
				encoded, _ := json.Marshal(tc.want)
				var want any
				if err := json.Unmarshal(encoded, &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual["enum"], want) {
					t.Fatalf("enum=%v, want %v", actual["enum"], want)
				}
			})
		}
		t.Run(surface+"/task-link-requirements", func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(specs["tasks_create"], &root); err != nil {
				t.Fatal(err)
			}
			condition := root["if"].(map[string]any)
			if !reflect.DeepEqual(condition["required"], []any{"work_item_id"}) || planningSchemaAt(t, condition, "work_item_id")["minLength"] != float64(1) {
				t.Fatal("linked-task condition must apply only to a nonempty work item ID")
			}
			then := root["then"].(map[string]any)
			if !reflect.DeepEqual(then["required"], []any{"work_item_revision", "stage_ids"}) || planningSchemaAt(t, then, "stage_ids")["minItems"] != float64(1) {
				t.Fatal("linked-task schema must require the revision and nonempty stages")
			}
		})
	}
}

func TestPlanningTaskCorrectionPreservesPlanAndCreatesOnlyOnce(t *testing.T) {
	e := newPlanningEnv(t)
	plan := protocol.WorkItemPlan{Title: "Improve settings", Intent: "Design, then independently review", Background: []protocol.SharedFact{{Kind: "observation", Text: "Duplicate settings entry", Source: "owner message"}}, Stages: []protocol.WorkItemStage{
		{StageID: "design", Title: "Design", Status: "planned"},
		{StageID: "review", Title: "Independent review", Status: "planned", DependsOn: []string{"design"}},
	}}
	created := e.call("work_items_create", workItemCreateArgs{InitialContext: initialFixtureContext(), Plan: plan, CommitSeq: 1, Reason: "Preserve the full concern"}, "")
	if !created.OK {
		t.Fatal(created.Error)
	}
	var result struct{ Outcome protocol.WorkItem }
	if err := json.Unmarshal(created.Result, &result); err != nil {
		t.Fatal(err)
	}
	w := result.Outcome
	args := map[string]any{
		"title": "Design settings", "workspace_name": "settings-design", "commit_seq": 2,
		"work_item_id": w.WorkItemID, "work_item_revision": w.Revision,
		"spec": map[string]any{"goal": "Deliver a settings design", "acceptance": []string{"Report explains design and includes files"},
			"workspace": map[string]any{"mode": "temporary"}, "deliverables": map[string]bool{"report": true, "files": true}},
	}
	bad := e.call("tasks_create", args, "")
	if bad.OK || bad.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(bad.Error.Message, `spec.deliverables`) || !strings.Contains(bad.Error.Message, `["report","files"]`) {
		t.Fatalf("object deliverables must explain the array shape: %+v", bad)
	}
	var details map[string]any
	if err := json.Unmarshal(bad.Error.Details, &details); err != nil || details["field"] != "spec.deliverables" {
		t.Fatalf("missing field details: %s (%v)", bad.Error.Details, err)
	}
	args["spec"].(map[string]any)["deliverables"] = []string{"report", "files"}
	args["spec"].(map[string]any)["scope"] = []string{"Design only"}
	args["commit_seq"] = 3
	bad = e.call("tasks_create", args, "")
	if bad.OK || bad.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(bad.Error.Message, "spec.scope: expected string, got array") || !strings.Contains(bad.Error.Message, "plain-text string") {
		t.Fatalf("scope array must explain the text shape: %+v", bad)
	}
	args["spec"].(map[string]any)["scope"] = "Design only"
	args["commit_seq"] = 4
	bad = e.call("tasks_create", args, "")
	if bad.OK || bad.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(bad.Error.Message, "stage_ids") {
		t.Fatalf("missing association must be explained: %+v", bad)
	}
	if n := e.tasks(); n != 0 {
		t.Fatalf("rejected calls created %d tasks", n)
	}
	args["stage_ids"] = []string{"design"}
	args["commit_seq"] = 5
	corrected := e.call("tasks_create", args, "")
	if !corrected.OK {
		t.Fatal(corrected.Error)
	}
	if n := e.tasks(); n != 1 {
		t.Fatalf("corrected call created %d tasks, want 1", n)
	}
	read := e.call("work_items_read", workItemReadArgs{WorkItemID: w.WorkItemID}, "")
	var item protocol.WorkItemView
	if !read.OK || json.Unmarshal(read.Result, &item) != nil {
		t.Fatalf("read corrected plan: %+v", read)
	}
	if !reflect.DeepEqual(item.Background, plan.Background) || len(item.Stages) != 2 || item.Stages[1].StageID != "review" || len(item.Stages[1].TaskIDs) != 0 || len(item.Stages[0].TaskIDs) != 1 {
		t.Fatalf("correction lost background, future review, or task association: %+v", item.WorkItem)
	}
}
