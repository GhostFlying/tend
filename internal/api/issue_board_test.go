package api

import (
	"net/http"
	"testing"

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
