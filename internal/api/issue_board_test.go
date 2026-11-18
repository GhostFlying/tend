package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueBoardHTTPStatusReceiptsCountsAndSettings(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	createOwnerIssueHTTP(t, e, token, issue.IssueID)
	issue = admitOwnerIssueHTTP(t, e, token, issue)
	command := e.envelope("issues.set_status", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress})
	moved := issueHTTP(t, e, token, command)
	status, response := e.op(token, e.envelope(command.Operation, command.Target, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInReview}))
	if status != http.StatusOK || response.Operation == nil || response.Operation.Error == nil || response.Operation.Error.Code != protocol.CodeStaleRevision || response.CurrentIssue == nil || response.CurrentIssue.Revision != moved.Revision {
		t.Fatalf("stale=%d %+v", status, response)
	}
	var saved protocol.IssueView
	if status := e.do("GET", "/api/v1/issues/"+issue.IssueID, token, nil, &saved); status != http.StatusOK || saved.WorkStatus != protocol.IssueInProgress {
		t.Fatal(status, saved)
	}
	var page protocol.IssuePage
	if status := e.do("GET", "/api/v1/issues?board=true&project=default&board_status=in_progress&limit=1&include_closed=true", token, nil, &page); status != http.StatusOK || len(page.Items) != 1 || page.Counts[protocol.IssueInProgress] != 1 || page.Counts[protocol.IssueTodo] != 0 || len(page.Counts) != 6 || page.Items[0].Board == nil {
		t.Fatalf("board=%d %+v", status, page)
	}
	var settings protocol.IssueNotificationSettings
	if status := e.do("GET", "/api/v1/issue-notification-settings", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	if status := e.do("GET", "/api/v1/issue-notification-settings", token, nil, &settings); status != http.StatusOK || settings.DelayMinutes != 3 {
		t.Fatal(status, settings)
	}
	settingsCommand := e.envelope("issues.set_notification_settings", protocol.Target{Type: "issue_settings"}, &settings.Revision, protocol.SetIssueNotificationSettingsPayload{DelayMinutes: 0})
	status, response = e.op(token, settingsCommand)
	if status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, response)
	}
	if status := e.do("GET", "/api/v1/issue-notification-settings", token, nil, &settings); status != http.StatusOK || settings.DelayMinutes != 0 || settings.Revision != 1 {
		t.Fatal(status, settings)
	}
}

func TestIssueBoardHTTPPaginatesByStageEntry(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	token := e.login(protocol.ScopeOwner)
	create := func(id, project string) protocol.Issue {
		t.Helper()
		payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Project: project, Title: "Board fixture", Intent: "Verify stage order"}}
		return issueHTTP(t, e, token, e.envelope("issues.create", protocol.Target{Type: "issue", ID: id}, nil, payload))
	}
	move := func(issue protocol.Issue, status protocol.IssueWorkStatus) protocol.Issue {
		t.Helper()
		payload := protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}
		return issueHTTP(t, e, token, e.envelope("issues.set_status", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, payload))
	}

	old := create("wi_00000000000000000000000001", "core")
	query := url.Values{
		"board": {"true"}, "board_status": {"backlog"}, "project": {"core"},
		"assignee_kind": {string(old.Assignee.Kind)}, "assignee_ref": {old.Assignee.Ref},
		"include_closed": {"true"}, "limit": {"1"},
	}
	readPage := func() protocol.IssuePage {
		t.Helper()
		var page protocol.IssuePage
		if status := e.do("GET", "/api/v1/issues?"+query.Encode(), token, nil, &page); status != http.StatusOK {
			t.Fatalf("board HTTP%d with %v: %+v", status, query, page)
		}
		return page
	}
	assertPage := func(page protocol.IssuePage, issue protocol.Issue, status protocol.IssueWorkStatus, at int64, source protocol.IssueStageEntryBasis) {
		t.Helper()
		if len(page.Items) != 1 {
			t.Fatalf("want one Issue, got %+v", page)
		}
		card := page.Items[0]
		if card.IssueID != issue.IssueID || card.ShortCode != issue.ShortCode || card.Board == nil ||
			card.Board.Status != status || card.Board.StageEnteredAt != at || card.Board.StageEntryBasis != source {
			t.Fatalf("want %s in %s at %d from %s, got %+v board=%+v", issue.IssueID, status, at, source, card, card.Board)
		}
	}
	assertPage(readPage(), old, protocol.IssueBacklog, old.CreatedAt, protocol.IssueStageEntryCreation)
	e.clock.Advance(time.Minute)
	old = move(admitOwnerIssueHTTP(t, e, token, old), protocol.IssueInProgress)
	oldEntry := old.UpdatedAt

	e.clock.Advance(time.Minute)
	a := create("wi_00000000000000000000000003", "core")
	b := create("wi_00000000000000000000000002", "core")
	other := create("wi_00000000000000000000000004", "web")
	e.clock.Advance(time.Minute)
	a = move(admitOwnerIssueHTTP(t, e, token, a), protocol.IssueInProgress)
	b = move(admitOwnerIssueHTTP(t, e, token, b), protocol.IssueInProgress)
	move(admitOwnerIssueHTTP(t, e, token, other), protocol.IssueInProgress)
	tie := a.UpdatedAt

	e.clock.Advance(time.Minute)
	old.Plan.Title = "Normal title edit"
	target := protocol.Target{Type: "issue", ID: old.IssueID}
	old = issueHTTP(t, e, token, e.envelope("issues.update", target, &old.Revision, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: old.AssignmentEpoch, Plan: old.Plan}))
	progress := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: old.AssignmentEpoch, Summary: "Normal progress", NextStep: "Keep working"}
	if status, response := e.op(token, e.envelope("issues.update_progress", target, &old.Revision, progress)); status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("progress update HTTP%d %+v", status, response)
	}

	query.Set("board_status", "in_progress")
	for index, issue := range []protocol.Issue{b, a, old} {
		page := readPage()
		entry := tie
		if issue.IssueID == old.IssueID {
			entry = oldEntry
		}
		assertPage(page, issue, protocol.IssueInProgress, entry, protocol.IssueStageEntryTransition)
		if more := index < 2; page.More != more || (page.Next != "") != more || page.Counts[protocol.IssueInProgress] != 3 {
			t.Fatalf("page %d pagination/counts changed: %+v", index+1, page)
		}
		query.Set("after", page.Next)
	}
	query.Del("after")
	e.clock.Advance(time.Minute)
	a = move(a, protocol.IssueInReview)
	query.Set("board_status", "in_review")
	assertPage(readPage(), a, protocol.IssueInReview, a.UpdatedAt, protocol.IssueStageEntryTransition)
	query.Set("board_status", "in_progress")
	assertPage(readPage(), b, protocol.IssueInProgress, tie, protocol.IssueStageEntryTransition)

	query.Set("group_review", "true")
	for index, issue := range []protocol.Issue{b, a, old} {
		page := readPage()
		entry := tie
		if issue.IssueID == old.IssueID {
			entry = oldEntry
		}
		assertPage(page, issue, issue.WorkStatus, entry, protocol.IssueStageEntryTransition)
		if more := index < 2; page.More != more || (page.Next != "") != more || page.Counts[protocol.IssueInProgress] != 3 || len(page.Counts) != 5 {
			t.Fatalf("grouped page %d pagination/counts changed: %+v", index+1, page)
		}
		query.Set("after", page.Next)
	}
	query.Del("after")
	e.clock.Advance(time.Minute)
	a = move(a, protocol.IssueInProgress)
	assertPage(readPage(), b, protocol.IssueInProgress, tie, protocol.IssueStageEntryTransition)
	query.Del("group_review")
	assertPage(readPage(), a, protocol.IssueInProgress, a.UpdatedAt, protocol.IssueStageEntryTransition)
}

func TestIssueCancellationHTTPSettledEntryPoints(t *testing.T) {
	for _, action := range []string{"cancel", "set_status"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			e.configure()
			token := e.login(protocol.ScopeOwner)
			issue := createOwnerIssueHTTP(t, e, token, "")
			var payload any = protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel settled Issue"}
			if action == "set_status" {
				payload = protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueWorkCancelled}
			}
			// Both real HTTP producers retain revision and epoch fences.
			staleEpoch := protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch + 1}
			if action == "set_status" {
				payload = protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: staleEpoch.ExpectedAssignmentEpoch, Status: protocol.IssueWorkCancelled}
			} else {
				payload = staleEpoch
			}
			status, response := e.op(token, e.envelope("issues."+action, protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, payload))
			if status != http.StatusOK || response.Operation == nil || response.Operation.Error == nil || response.Operation.Error.Code != protocol.CodeStaleRevision {
				t.Fatal(status, response)
			}
			if action == "set_status" {
				payload = protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueWorkCancelled}
			} else {
				payload = protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel settled Issue"}
			}
			cancelled := issueHTTP(t, e, token, e.envelope("issues."+action, protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, payload))
			if cancelled.State != protocol.IssueCancelled || cancelled.Revision != issue.Revision+1 || cancelled.Closure != nil {
				t.Fatal(cancelled)
			}
			status, response = e.op(token, e.envelope("issues."+action, protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, payload))
			if status != http.StatusOK || response.Operation == nil || response.Operation.Error == nil || response.Operation.Error.Code != protocol.CodeStaleRevision {
				t.Fatal(status, response)
			}
		})
	}
}
