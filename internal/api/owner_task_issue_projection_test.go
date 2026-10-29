package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestOwnerTaskIssueProjectionAcrossActualReadsAndStale(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	parent := createOwnerIssueHTTP(t, e, token, "")
	child := createOwnerIssueHTTP(t, e, token, parent.IssueID)
	spec := protocol.OwnerTaskSpec{Goal: "Investigate the delegated goal", Acceptance: []string{"Return a bounded report"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}
	status, op := e.op(token, e.envelope("issues.create_task", protocol.Target{Type: "issue", ID: child.IssueID}, &child.Revision, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Title: "No-stage delegated task", Spec: spec}))
	if status != http.StatusOK || op.Operation == nil || op.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("HTTP%d operation%+v", status, op.Operation)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Operation.Result, &task); err != nil {
		t.Fatal(err)
	}
	read := func(id string) map[string]json.RawMessage {
		t.Helper()
		var out map[string]json.RawMessage
		if status := e.do("GET", "/api/v1/tasks/"+id, token, nil, &out); status != http.StatusOK {
			t.Fatal(status)
		}
		return out
	}
	original := read(task.TaskID)
	check := func(row map[string]json.RawMessage) {
		t.Helper()
		var id, title string
		_ = json.Unmarshal(row["primary_issue_id"], &id)
		_ = json.Unmarshal(row["primary_issue_title"], &title)
		if id != child.IssueID || title != "Renamed child intent" {
			t.Fatalf("canonical primary not projected: id%q title%q row%s", id, title, row)
		}
	}
	// An explicit result link is not another responsibility, including on a legacy item.
	legacy := e.task("Historical task")
	historical := read(legacy)
	var historicalIDs []string
	if err := json.Unmarshal(historical["work_item_ids"], &historicalIDs); err != nil || len(historicalIDs) != 1 {
		t.Fatal(historicalIDs, err)
	}
	var item protocol.WorkItem
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		item, err = store.GetIssue(t.Context(), tx.Q(), historicalIDs[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Existing app/tool link fixture: this command is intentionally not an
	// owner HTTP registration. Do not add a public write to test a read view.
	link := e.envelope("work_items.link_task", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &item.Revision, protocol.LinkWorkItemTaskPayload{TaskID: task.TaskID, StageIDs: []string{"initial"}})
	linked, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &link, app.LinkIssueTask{})
	if err != nil || linked.Status != protocol.OpSucceeded {
		t.Fatal(linked, err)
	}
	child = issueDetailHTTP(t, e, token, child.IssueID).Issue
	plan := child.Plan
	plan.Title = "Renamed child intent"
	child = issueHTTP(t, e, token, e.envelope("issues.update", protocol.Target{Type: "issue", ID: child.IssueID}, &child.Revision, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: child.AssignmentEpoch}))
	detail := read(task.TaskID)
	check(detail)
	var ids []string
	if err := json.Unmarshal(detail["issue_ids"], &ids); err != nil || len(ids) != 1 || ids[0] != child.IssueID {
		t.Fatal("result link became responsibility", ids, err)
	}
	if !bytes.Equal(original["spec"], detail["spec"]) || !bytes.Equal(original["runs"], detail["runs"]) {
		t.Fatal("projection changed frozen task/run contract")
	}
	for _, path := range []string{"/api/v1/snapshot", "/api/v1/tasks", "/api/v1/tasks?all=1&limit=1"} {
		found := false
		for {
			var page struct {
				Tasks []map[string]json.RawMessage `json:"tasks"`
				Next  string                       `json:"next"`
			}
			if status := e.do("GET", path, token, nil, &page); status != http.StatusOK {
				t.Fatal(path, status)
			}
			for _, row := range page.Tasks {
				var id string
				_ = json.Unmarshal(row["task_id"], &id)
				if id == task.TaskID {
					check(row)
					found = true
				}
			}
			if page.Next == "" {
				break
			}
			path = "/api/v1/tasks?all=1&limit=1&before=" + page.Next
		}
		if !found {
			t.Fatal("canonical task absent", path)
		}
	}
	var revision int64
	if err := json.Unmarshal(detail["control_revision"], &revision); err != nil {
		t.Fatal(err)
	}
	target := protocol.Target{Type: "task", ID: task.TaskID}
	if status, rated := e.op(token, e.envelope("tasks.rate", target, &revision, protocol.RateTaskPayload{Rating: protocol.RatingP0})); status != http.StatusOK || rated.Operation == nil || rated.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, rated)
	}
	status, stale := e.op(token, e.envelope("tasks.rate", target, &revision, protocol.RateTaskPayload{Rating: protocol.RatingP2}))
	if status != http.StatusOK || stale.Current == nil || stale.Operation == nil || stale.Operation.Error == nil || stale.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(status, stale)
	}
	raw, _ := json.Marshal(stale.Current)
	var current map[string]json.RawMessage
	_ = json.Unmarshal(raw, &current)
	check(current)
	// A real owner cancellation moves it to the original finished/read path;
	// neither projection nor pagination fabricates task execution history.
	currentRevision := stale.Current.ControlRevision
	if status, cancelled := e.op(token, e.envelope("tasks.cancel", target, &currentRevision, map[string]any{})); status != http.StatusOK || cancelled.Operation == nil || cancelled.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, cancelled)
	}
	for _, path := range []string{"/api/v1/tasks?finished=1", "/api/v1/tasks?all=1&lifecycle=cancelled&limit=1"} {
		var page struct {
			Tasks []map[string]json.RawMessage `json:"tasks"`
		}
		if status := e.do("GET", path, token, nil, &page); status != http.StatusOK {
			t.Fatal(status)
		}
		found := false
		for _, row := range page.Tasks {
			var id string
			_ = json.Unmarshal(row["task_id"], &id)
			if id == task.TaskID {
				check(row)
				found = true
			}
		}
		if !found {
			t.Fatal("finished projection lost primary", path, page)
		}
	}
	terminal := read(task.TaskID)
	if !bytes.Equal(original["spec"], terminal["spec"]) || !bytes.Equal(original["runs"], terminal["runs"]) {
		t.Fatal("owner control/view changed frozen input or fabricated a run")
	}
	historical = read(legacy)
	for _, key := range []string{"primary_issue_id", "primary_issue_title", "issue_ids"} {
		if _, present := historical[key]; present {
			t.Fatal("historical identity fabricated canonical Issue", key, historical)
		}
	}
	var afterIDs []string
	_ = json.Unmarshal(historical["work_item_ids"], &afterIDs)
	if len(afterIDs) != 1 || afterIDs[0] != historicalIDs[0] {
		t.Fatal("historical identity rewritten", afterIDs)
	}
}
