package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueProgressHTTPHistoryReplayAndActivity(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	parent := createOwnerIssueHTTP(t, e, token, "")
	issue := createOwnerIssueHTTP(t, e, token, parent.IssueID)
	path := "/api/v1/issues/" + issue.IssueID
	initial := issueDetailHTTP(t, e, token, issue.IssueID)
	if initial.Progress != nil || initial.Conversation == nil || !initial.Conversation.CanSend || len(initial.Ancestors) != 1 || initial.Ancestors[0].IssueID != parent.IssueID {
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
	if replay.Operation == nil || replay.Operation.Error == nil || replay.Operation.Error.Code != protocol.CodeStaleRevision || e.count(`SELECT count(*) FROM issue_progress_revisions`) != count {
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
	for i, entry := range first.Items {
		if entry.Kind != "issue.progress_updated" || entry.Progress == nil || entry.Progress.Summary != fmt.Sprintf("Progress %d", i+3) {
			t.Fatalf("activity lost its matching assessment: %#v", entry)
		}
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

func TestIssueCancellationReadUsesOriginalIntent(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	for _, reason := range []string{"First cancellation", "Second cancellation"} {
		issue = issueHTTP(t, e, token, e.envelope("issues.cancel", target, &issue.Revision, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: reason}))
		view := issueDetailHTTP(t, e, token, issue.IssueID)
		if view.State != protocol.IssueCancelled || view.CancellationReason != reason {
			t.Fatal(view)
		}
		issue = issueHTTP(t, e, token, e.envelope("issues.reopen", target, &issue.Revision, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Resume work"}))
		if got := issueDetailHTTP(t, e, token, issue.IssueID).CancellationReason; got != "" {
			t.Fatal(got)
		}
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

func TestIssueProgressPreservesBoardAndChildActivityResults(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	parent := createOwnerIssueHTTP(t, e, token, "")
	child := createOwnerIssueHTTP(t, e, token, parent.IssueID)
	move := func(status protocol.IssueWorkStatus) {
		if status == protocol.IssueTodo || ((child.BoardStatus() == protocol.IssueBacklog) && (status != protocol.IssueBacklog && status != protocol.IssueWorkCancelled)) {
			child = admitOwnerIssueHTTP(t, e, token, child)
			if status == protocol.IssueTodo {
				return
			}
		}

		t.Helper()
		child = issueHTTP(t, e, token, e.envelope("issues.set_status", protocol.Target{Type: "issue", ID: child.IssueID}, &child.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Status: status}))
	}
	read := func(id string) protocol.IssueActivityPage {
		t.Helper()
		var page protocol.IssueActivityPage
		if status := e.do("GET", "/api/v1/issues/"+id+"/activity", token, nil, &page); status != http.StatusOK {
			t.Fatal(status)
		}
		return page
	}
	move(protocol.IssueInReview)
	due := e.count(`SELECT due_at FROM issue_status_notifications WHERE issue_id=?`, child.IssueID)
	e.clock.Advance(time.Minute)
	_, out := e.op(token, e.envelope("issues.update_progress", protocol.Target{Type: "issue", ID: child.IssueID}, &child.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Summary: "Ready for review", NextStep: "Check the evidence"}))
	if out.Operation == nil || out.Operation.Status != protocol.OpSucceeded {
		t.Fatal(out)
	}
	view := issueDetailHTTP(t, e, token, child.IssueID)
	if view.State != child.State || view.WorkStatus != protocol.IssueInReview || e.count(`SELECT due_at FROM issue_status_notifications WHERE issue_id=?`, child.IssueID) != due {
		t.Fatal("summary changed board responsibility or notification window", view)
	}
	child = view.Issue
	for _, entry := range read(parent.IssueID).Items {
		if entry.IssueID == child.IssueID {
			t.Fatal("child nonterminal status leaked into parent history", entry)
		}
	}
	found := false
	for _, entry := range read(child.IssueID).Items {
		found = found || entry.Kind == "issue.status_changed"
	}
	if !found {
		t.Fatal("own board event missing")
	}
	move(protocol.IssueDone)
	move(protocol.IssueTodo)
	move(protocol.IssueWorkCancelled)
	var results []string
	for _, entry := range read(parent.IssueID).Items {
		if entry.IssueID == child.IssueID {
			results = append(results, entry.Title)
		}
	}
	if len(results) != 2 || results[0] != child.Plan.Title+" · 关闭了 Issue" || results[1] != child.Plan.Title+" · 取消了 Issue" {
		t.Fatal("child results were omitted or rewritten by later state", results)
	}
}

func TestIssueChildActivityRetainsHistoricalParent(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	a := createOwnerIssueHTTP(t, e, token, "")
	child := createOwnerIssueHTTP(t, e, token, a.IssueID)
	target := protocol.Target{Type: "issue", ID: child.IssueID}
	move := func(status protocol.IssueWorkStatus) {
		if status == protocol.IssueTodo || ((child.BoardStatus() == protocol.IssueBacklog) && (status != protocol.IssueBacklog && status != protocol.IssueWorkCancelled)) {
			child = admitOwnerIssueHTTP(t, e, token, child)
			if status == protocol.IssueTodo {
				return
			}
		}

		child = issueHTTP(t, e, token, e.envelope("issues.set_status", target, &child.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Status: status}))
	}
	read := func(id string) []protocol.IssueActivityEntry {
		var page protocol.IssueActivityPage
		if code := e.do("GET", "/api/v1/issues/"+id+"/activity", token, nil, &page); code != http.StatusOK {
			t.Fatal(code)
		}
		var out []protocol.IssueActivityEntry
		for _, item := range page.Items {
			if item.IssueID == child.IssueID {
				out = append(out, item)
			}
		}
		return out
	}
	move(protocol.IssueDone)
	move(protocol.IssueTodo)
	b := createOwnerIssueHTTP(t, e, token, "")
	beforeA, beforeB := read(a.IssueID), read(b.IssueID)
	if len(beforeA) != 1 || len(beforeB) != 0 {
		t.Fatal(beforeA, beforeB)
	}
	plan := child.Plan
	plan.Title = "Renamed continuing work"
	child = issueHTTP(t, e, token, e.envelope("issues.update", target, &child.Revision, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Plan: plan}))
	child = issueHTTP(t, e, token, e.envelope("issues.reparent", target, &child.Revision, protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Reason: "Move continuing responsibility"}, ParentIssueID: b.IssueID, ExpectedParentRevision: &b.Revision}))
	afterA, afterB := read(a.IssueID), read(b.IssueID)
	t.Logf("before reparent: old parent=%+v; new parent=%+v", beforeA, beforeB)
	t.Logf("after reparent: old parent=%+v; new parent=%+v", afterA, afterB)
	if !reflect.DeepEqual(afterA, beforeA) || !reflect.DeepEqual(afterB, beforeB) {
		t.Fatal("reparent or rename rewrote a historical completion event")
	}
	move(protocol.IssueDone)
	if !reflect.DeepEqual(read(a.IssueID), beforeA) {
		t.Fatal("new closure rewrote old parent history")
	}
	current := read(b.IssueID)
	if len(current) != 1 || current[0].Title != plan.Title+" · 关闭了 Issue" || current[0].ID == beforeA[0].ID || current[0].Position <= beforeA[0].Position {
		t.Fatal(current)
	}
}

func TestIssueActivityReadableProjectsAndRefreshesChangedTurns(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	conversation := issueDetailHTTP(t, e, token, issue.IssueID).Conversation.ConversationID
	ctx := t.Context()
	write := func(fn func(*store.Tx) error) {
		t.Helper()
		if err := e.store.Write(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	event := func(tx *store.Tx, id, kind, body string) error {
		_, err := tx.InsertEvent(ctx, store.EventRow{
			EventID: e.ids.New(protocol.PrefixEvent), Source: "test", ObjectType: "message",
			ObjectID: id, Kind: kind, BodyJSON: body, CreatedAt: e.clock.Now().UnixMilli(),
		})
		return err
	}
	insert := func(tx *store.Tx, id, author, kind, replyTo, body string) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{
			ConversationID: conversation, MessageID: id, Author: author, Kind: kind,
			ReplyTo: replyTo, Body: body, CreatedAt: e.clock.Now().UnixMilli(),
		})
		if err != nil {
			return err
		}
		return event(tx, id, "conversation.message", "{}")
	}
	admit := func(tx *store.Tx, id string) error {
		return event(tx, id, "conversation.message_state", `{"state":"processing"}`)
	}
	write(func(tx *store.Tx) error {
		for i := range 55 {
			id := fmt.Sprintf("owner-%02d", i)
			if err := insert(tx, id, "owner", "", "", id); err != nil {
				return err
			}
			if i != 54 {
				if err := admit(tx, id); err != nil {
					return err
				}
			}
		}
		for i := range 51 {
			id := fmt.Sprintf("tool-%02d", i)
			if err := insert(tx, id, "main_agent", store.KindToolUse, "owner-53", ""); err != nil {
				return err
			}
			if err := tx.PutToolUse(ctx, store.ToolUseRow{
				MessageID: id, ThreadID: "test-thread", TurnID: "test-turn", ItemID: id,
				Tool: "exec_command", Input: "actual input", Output: strings.Repeat("actual output", 2000),
				Status: "completed", UpdatedAt: e.clock.Now().UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return insert(tx, "last-reply", "main_agent", "", "owner-53", "正文可见")
	})
	path := "/api/v1/issues/" + issue.IssueID + "/activity?view=readable"
	read := func(query string) protocol.IssueActivityPage {
		t.Helper()
		var page protocol.IssueActivityPage
		if status := e.do("GET", path+query, token, nil, &page); status != http.StatusOK {
			t.Fatal(status)
		}
		return page
	}
	first := read("")
	if !first.Readable || len(first.Items) != 50 || !first.More {
		t.Fatalf("head length=%d more=%t readable=%t", len(first.Items), first.More, first.Readable)
	}
	last := first.Items[len(first.Items)-1].Block
	if last == nil || last.Trigger.MessageID != "owner-53" || last.Process == nil ||
		!last.Process.HasTools || len(last.Process.Items) != 0 ||
		len(last.Items) != 1 || last.Items[0].Body != "正文可见" {
		t.Fatal("tool detail displaced readable prose", last)
	}
	var detail protocol.IssueTranscriptBlock
	if status := e.do("GET", "/api/v1/conversation/blocks/owner-53?projection=issues", token, nil, &detail); status != http.StatusOK ||
		len(detail.Items) != 50 || detail.Items[0].Tool == nil || !strings.HasPrefix(detail.Items[0].Tool.Output, "actual output") {
		t.Fatal("detail lost original tool output", status)
	}
	old := read(fmt.Sprintf("&before=%d", first.NextBefore))
	if old.More || len(old.Items) == 0 {
		t.Fatal(old)
	}
	write(func(tx *store.Tx) error {
		if err := admit(tx, "owner-54"); err != nil {
			return err
		}
		if err := insert(tx, "old-turn-reply", "main_agent", "", "owner-00", "新的历史回复"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id='owner-01'"); err != nil {
			return err
		}
		return event(tx, "owner-01", "conversation.message_state", `{"state":"interrupted","withdrawn":true}`)
	})
	delta := read(fmt.Sprintf("&since=%d", first.EventSeq))
	if len(delta.Items) != 2 || !reflect.DeepEqual(delta.Removed, []string{"block:owner-01"}) ||
		delta.Items[0].Block.Items[0].Body != "新的历史回复" ||
		delta.Items[1].Block.Trigger.MessageID != "owner-54" {
		t.Fatalf("unexpected delta: %+v", delta)
	}
	idle := read(fmt.Sprintf("&since=%d", delta.EventSeq))
	if len(idle.Items) != 0 || len(idle.Removed) != 0 {
		t.Fatal("unchanged pages were retransmitted", idle)
	}

	write(func(tx *store.Tx) error {
		for _, id := range []string{"large-1", "large-2"} {
			if err := insert(tx, id, "owner", "", "", strings.Repeat("z", 300<<10)); err != nil {
				return err
			}
			if err := admit(tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	large := read("")
	if len(large.Items) != 1 || !large.More || large.Items[0].Block.Trigger.MessageID != "large-2" {
		t.Fatal("page did not stop at byte budget")
	}
	largeEarlier := read(fmt.Sprintf("&before=%d", large.NextBefore))
	if len(largeEarlier.Items) == 0 || largeEarlier.Items[len(largeEarlier.Items)-1].Block.Trigger.MessageID != "large-1" {
		t.Fatal("byte budget skipped the next activity")
	}
}

func TestIssueActivityReadableReadsCoalescedPublicProcessChanges(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	conversation := issueDetailHTTP(t, e, token, issue.IssueID).Conversation.ConversationID
	ctx, now := t.Context(), e.clock.Now().UnixMilli()
	const source = "process-trigger"
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{
			ConversationID: conversation, MessageID: source, Author: "system", Body: "background work", CreatedAt: now,
		}); err != nil {
			return err
		}
		for _, event := range []struct{ kind, body string }{
			{"conversation.message", "{}"}, {"conversation.message_state", `{"state":"processing"}`},
		} {
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: e.ids.New(protocol.PrefixEvent), Source: "test",
				ObjectType: "message", ObjectID: source, Kind: event.kind, BodyJSON: event.body, CreatedAt: now}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO background_batches(batch_id,group_key,work_item_id,message_id,status,created_at,frozen_at)
 VALUES('process-batch','group',?,?,'frozen',?,?)`, issue.IssueID, source, now, now); err != nil {
			return err
		}
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: "process-native", MessageID: source,
			ThreadID: "process-thread", TurnID: "process-turn", Status: store.TurnStarted}, now)
	}); err != nil {
		t.Fatal(err)
	}
	read := func(since int64) protocol.IssueActivityPage {
		t.Helper()
		var page protocol.IssueActivityPage
		path := fmt.Sprintf("/api/v1/issues/%s/activity?view=readable&since=%d", issue.IssueID, since)
		if status := e.do("GET", path, token, nil, &page); status != http.StatusOK {
			t.Fatal(status)
		}
		return page
	}
	before := read(0)
	for _, status := range []string{"running", "completed"} {
		req := e.envelope("conversation.record_tool_use", protocol.Target{Type: "conversation"}, nil, map[string]string{
			"reply_to": source, "turn_id": "process-turn", "item_id": "process-tool",
			"tool": "exec_command", "input": "{}", "output": "actual output", "status": status,
		})
		principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "harness-process", Thread: "process-thread"}
		if op, err := e.app.Execute(ctx, principal, &req, app.RecordToolUse{}); err != nil || op.Status != protocol.OpSucceeded {
			t.Fatalf("record tool use: %+v %v", op, err)
		}
	}
	delta := read(before.EventSeq)
	if len(delta.Items) != 1 || delta.Items[0].ID != "block:"+source || delta.Items[0].Block.Process == nil ||
		!delta.Items[0].Block.Process.HasTools || len(delta.Items[0].Block.Process.Items) != 0 {
		t.Fatal("coalesced process revision did not refresh the readable turn", delta)
	}
	var detail protocol.IssueTranscriptBlock
	if status := e.do("GET", "/api/v1/conversation/blocks/"+source, token, nil, &detail); status != http.StatusOK ||
		detail.Process == nil || len(detail.Process.Items) != 1 || detail.Process.Items[0].Tool.Status != "completed" {
		t.Fatal("updated process detail failed", status, detail)
	}
}

func TestIssueActivityReadableRefreshesBothTurnsAfterToolRebind(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	conversation := issueDetailHTTP(t, e, token, issue.IssueID).Conversation.ConversationID
	const previous = "review-previous-turn"
	const current = "review-corrected-turn"
	const native = "review-native-callback"
	ctx := t.Context()
	now := e.clock.Now().UnixMilli()
	record := store.ToolUseRow{
		ThreadID: "review-thread", TurnID: "review-turn", ItemID: "review-item",
		Tool: "tasks_read", Input: "{}", Status: "running", UpdatedAt: now,
	}
	event := func(tx *store.Tx, id, kind, body string) error {
		_, err := tx.InsertEvent(ctx, store.EventRow{EventID: e.ids.New(protocol.PrefixEvent),
			Source: "review-fixture", ObjectType: "message", ObjectID: id, Kind: kind, BodyJSON: body, CreatedAt: now})
		return err
	}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{previous, current} {
			if _, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: id, Author: "owner", Body: id, CreatedAt: now}); err != nil {
				return err
			}
			if err := event(tx, id, "conversation.message", "{}"); err != nil {
				return err
			}
			if err := event(tx, id, "conversation.message_state", `{"state":"processing"}`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	args := map[string]string{
		"turn_id": record.TurnID, "item_id": record.ItemID, "reply_to": previous,
		"tool": record.Tool, "input": record.Input, "status": "running",
	}
	envelope := e.envelope("conversation.record_tool_use", protocol.Target{Type: "conversation"}, nil, args)
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "review-harness", Thread: record.ThreadID}
	if result, err := e.app.Execute(ctx, principal, &envelope, app.RecordToolUse{}); err != nil || result.Status != protocol.OpSucceeded {
		t.Fatalf("actual RecordToolUse: result=%+v err=%v", result, err)
	}
	activity := func(since int64) protocol.IssueActivityPage {
		t.Helper()
		var page protocol.IssueActivityPage
		path := fmt.Sprintf("/api/v1/issues/%s/activity?view=readable&since=%d", issue.IssueID, since)
		if code := e.do("GET", path, token, nil, &page); code != http.StatusOK {
			t.Fatal(code)
		}
		return page
	}
	projections := func(p protocol.IssueActivityPage) map[string]bool {
		items := make(map[string]bool)
		for _, entry := range p.Items {
			if b := entry.Block; b != nil {
				items[b.Trigger.MessageID] = b.Process != nil && b.Process.HasTools
			}
		}
		return items
	}
	first := activity(0)
	before := projections(first)
	if !before[previous] || before[current] {
		t.Fatalf("invalid precondition: %v", before)
	}
	bound, _, err := e.app.BindMainToolSource(ctx, native, current, "review-new-request", record)
	if err != nil || bound != current {
		t.Fatalf("actual BindMainToolSource: source=%q err=%v", bound, err)
	}
	full := projections(activity(0))
	changed := activity(first.EventSeq)
	delta := projections(changed)
	if full[previous] || !full[current] || !delta[current] {
		t.Fatalf("invalid updated source fixture: full=%v delta=%v", full, delta)
	}
	if old, included := delta[previous]; !included || old {
		t.Fatalf("the prior turn must be invalidated with empty tools; delta=%v", delta)
	}

	replayed, _, err := e.app.BindMainToolSource(ctx, native, previous, "replayed-request", record)
	if err != nil || replayed != current || len(activity(changed.EventSeq).Items) != 0 {
		t.Fatalf("native replay changed the original binding: source=%q err=%v", replayed, err)
	}
}
