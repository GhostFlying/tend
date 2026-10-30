package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueProgressHTTPHistoryReplayAndActivity(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	parent := createOwnerIssueHTTP(t, e, token, "")
	issue := createOwnerIssueHTTP(t, e, token, parent.IssueID)
	path := "/api/v1/issues/" + issue.IssueID
	initial := issueDetailHTTP(t, e, token, issue.IssueID)
	if initial.Progress != nil || initial.Conversation == nil || initial.Conversation.CanSend || len(initial.Ancestors) != 1 || initial.Ancestors[0].IssueID != parent.IssueID {
		t.Fatalf("initial=%+v", initial)
	}
	env := e.envelope("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "调查结论已确认", NextStep: "独立验收"})
	code, out := e.op(token, env)
	if code != http.StatusOK || out.Operation == nil || out.Operation.Status != protocol.OpSucceeded {
		t.Fatal(code, out)
	}
	var progress protocol.IssueProgress
	if err := json.Unmarshal(out.Operation.Result, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.IssueRevision != 2 || progress.UpdatedBy.Kind != protocol.IssueAssigneeOwner {
		t.Fatal(progress)
	}
	count := e.count(`SELECT count(*) FROM issue_progress_revisions`)
	_, replay := e.op(token, env)
	if replay.Operation == nil || replay.Operation.Status != protocol.OpSucceeded || e.count(`SELECT count(*) FROM issue_progress_revisions`) != count {
		t.Fatal(replay)
	}
	view := issueDetailHTTP(t, e, token, issue.IssueID)
	if view.ContextRevision != initial.ContextRevision {
		t.Fatal("assessment changed execution context", initial.ContextRevision, view.ContextRevision)
	}
	plan := view.Plan
	plan.Title = "Clarified goal"
	issueHTTP(t, e, token, e.envelope("issues.update", env.Target, &view.Revision, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	view = issueDetailHTTP(t, e, token, issue.IssueID)
	if view.Progress == nil || view.Progress.Summary != progress.Summary || view.Revision != 3 {
		t.Fatal(view)
	}
	for revision := int64(1); revision <= 3; revision++ {
		var historical protocol.IssueView
		if code := e.do("GET", fmt.Sprintf("%s?revision=%d", path, revision), token, nil, &historical); code != http.StatusOK {
			t.Fatal(code)
		}
		if historical.Conversation != nil || (revision == 1 && historical.Progress != nil) || (revision > 1 && (historical.Progress == nil || historical.Progress.IssueRevision != 2)) {
			t.Fatal(historical)
		}
	}
	// Same revision, wrong responsibility epoch must not write a new assessment.
	bad := e.envelope(env.Operation, env.Target, &view.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch + 1, Summary: "wrong"})
	_, failed := e.op(token, bad)
	if failed.Operation == nil || failed.Operation.Error == nil || failed.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(failed)
	}
	_, failed = e.op(token, e.envelope(env.Operation, env.Target, &view.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: strings.Repeat("界", 501)}))
	if failed.Operation == nil || failed.Operation.Error == nil || failed.Operation.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal(failed)
	}
	_, missing := e.op(token, e.envelope(env.Operation, env.Target, &view.Revision, map[string]any{"expected_assignment_epoch": issue.AssignmentEpoch}))
	if missing.Operation == nil || missing.Operation.Error == nil || missing.Operation.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("implicit clear accepted", missing)
	}
	for i := 0; i < 23; i++ {
		view = issueDetailHTTP(t, e, token, issue.IssueID)
		_, op := e.op(token, e.envelope(env.Operation, env.Target, &view.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: fmt.Sprintf("Progress %d", i)}))
		if op.Operation == nil || op.Operation.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	var first protocol.IssueActivityPage
	if code := e.do("GET", path+"/activity", token, nil, &first); code != http.StatusOK || len(first.Items) != 20 || !first.More {
		t.Fatal(code, first)
	}
	// Inserting after the first page must not move the earlier-page cursor.
	view = issueDetailHTTP(t, e, token, issue.IssueID)
	e.op(token, e.envelope(env.Operation, env.Target, &view.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Newer"}))
	var earlier protocol.IssueActivityPage
	if code := e.do("GET", fmt.Sprintf("%s/activity?before=%d", path, first.NextBefore), token, nil, &earlier); code != http.StatusOK {
		t.Fatal(code)
	}
	ids := map[string]bool{}
	for _, entry := range first.Items {
		ids[entry.ID] = true
	}
	for _, entry := range earlier.Items {
		if ids[entry.ID] || entry.Position >= first.NextBefore {
			t.Fatal("overlapping cursor", entry)
		}
	}
	if e.do("GET", path+"/activity?before=-1", token, nil, nil) != http.StatusBadRequest {
		t.Fatal("negative cursor accepted")
	}
	var results protocol.IssueResultPage
	if code := e.do("GET", path+"/results", token, nil, &results); code != http.StatusOK || len(results.Items) != 0 {
		t.Fatal(code, results)
	}
	if e.do("GET", path+"/activity", "", nil, nil) != http.StatusUnauthorized {
		t.Fatal("activity is not owner-only")
	}
}

func TestIssueActivityRetainsAttentionLifecycleFacts(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	_, created := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Provide acceptance device"}))
	if created.Operation == nil || created.Operation.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var attention protocol.Attention
	if err := json.Unmarshal(created.Operation.Result, &attention); err != nil {
		t.Fatal(err)
	}
	_, confirmed := e.op(token, e.envelope("attentions.confirm", protocol.Target{Type: "attention", ID: attention.AttentionID}, &attention.Revision, protocol.ConfirmAttentionPayload{Summary: "Device is ready"}))
	if confirmed.Operation == nil || confirmed.Operation.Status != protocol.OpSucceeded {
		t.Fatal(confirmed)
	}
	_, notice := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "information", Title: "Original notice"}))
	if notice.Operation == nil || notice.Operation.Status != protocol.OpSucceeded {
		t.Fatal(notice)
	}
	var info protocol.Attention
	if err := json.Unmarshal(notice.Operation.Result, &info); err != nil {
		t.Fatal(err)
	}
	_, updated := e.op(token, e.envelope("attentions.update", protocol.Target{Type: "attention", ID: info.AttentionID}, &info.Revision, protocol.UpdateAttentionPayload{Title: "Revised notice"}))
	if updated.Operation == nil || updated.Operation.Status != protocol.OpSucceeded {
		t.Fatal(updated)
	}
	var activity protocol.IssueActivityPage
	if status := e.do("GET", "/api/v1/issues/"+issue.IssueID+"/activity", token, nil, &activity); status != http.StatusOK {
		t.Fatal(status)
	}
	var titles []string
	for _, entry := range activity.Items {
		if entry.Kind == "attention.changed" {
			titles = append(titles, entry.Title)
		}
	}
	if len(titles) != 4 || titles[2] != "通知：Original notice" || titles[3] != "通知：Revised notice" || titles[0] != "新增或更新了待处理事项：Provide acceptance device" || titles[1] != "待处理事项已解决：Provide acceptance device" {
		t.Fatal(activity)
	}
}
