package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueBoardWaitingOnMeUsesOwnerInbox(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	const otherLogin = "other-owner@example.com"
	e.peer.set(netx.Identity{User: otherLogin})
	var otherSession protocol.SessionView
	if status := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeOwner}, &otherSession); status != http.StatusOK || otherSession.Login != otherLogin {
		t.Fatal(status, otherSession.Login)
	}
	e.peer.set(netx.Identity{User: ownerLogin})
	move := func(issue protocol.Issue, status protocol.IssueWorkStatus) protocol.Issue {
		t.Helper()
		return issueHTTP(t, e, token, e.envelope("issues.set_status", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}))
	}
	active := func() protocol.Issue {
		return move(admitOwnerIssueHTTP(t, e, token, createOwnerIssueHTTP(t, e, token, "")), protocol.IssueInProgress)
	}
	running := active()
	review := move(active(), protocol.IssueInReview)
	later := active()
	system := active()
	e.peer.set(netx.Identity{User: otherLogin})
	outsider := createOwnerIssueHTTP(t, e, otherSession.Token, "")
	e.peer.set(netx.Identity{User: ownerLogin})
	child := createOwnerIssueHTTP(t, e, token, system.IssueID)

	createAttention := func(issueID, kind, title string) protocol.Attention {
		t.Helper()
		_, response := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{IssueID: issueID, Kind: kind, Title: title}))
		if response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
			t.Fatalf("create attention: %+v", response.Operation)
		}
		var a protocol.Attention
		if err := json.Unmarshal(response.Operation.Result, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	putPlanner := func(issueID, id, responsible, kind, state string) protocol.PlannerEntry {
		t.Helper()
		payload := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{IssueID: issueID, AttentionID: id, Kind: kind, Title: "Plan the next action", Responsible: responsible, Status: state, Reason: "Explicit planner responsibility"}}
		_, response := e.op(token, e.envelope("planner.entries_put", protocol.Target{Type: "planner_entry"}, nil, payload))
		if response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
			t.Fatalf("put planner entry: %+v", response.Operation)
		}
		var entry protocol.PlannerEntry
		if err := json.Unmarshal(response.Operation.Result, &entry); err != nil {
			t.Fatal(err)
		}
		return entry
	}
	first := createAttention(running.IssueID, "action", "Check the draft")
	second := createAttention(running.IssueID, "action", "Review the evidence")
	createAttention(later.IssueID, "action", "Read the second result")
	createAttention(system.IssueID, "information", "System queue has changed")
	internal := createAttention(system.IssueID, "decision", "Internal Agent question")
	other := createAttention(outsider.IssueID, "action", "Assigned to the other owner")
	createAttention(child.IssueID, "action", "Read the child decision")
	createAttention("", "action", "Standalone Inbox item")
	putPlanner(running.IssueID, first.AttentionID, ownerLogin, "user_action", "open")
	putPlanner(review.IssueID, "", ownerLogin, "user_action", "blocked")
	putPlanner(system.IssueID, internal.AttentionID, ownerLogin, "user_action", "open")
	putPlanner(system.IssueID, "", ownerLogin, "issue_priority", "open")
	putPlanner(outsider.IssueID, "", otherLogin, "user_action", "open")

	// Seed the current routing snapshots independently of the HTTP reads. Owner
	// actions created via HTTP already cover unrouted legacy Inbox entries.
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, route := range []struct {
			attention protocol.Attention
			kind, ref string
			issue     protocol.Issue
		}{{internal, "agent", "dev/worker", system}, {other, "owner", otherLogin, outsider}} {
			current, err := store.GetAttentionResponsibility(t.Context(), tx.Q(), route.attention.AttentionID)
			if err != nil {
				return err
			}
			var revision int64
			if current != nil {
				revision = current.Revision
			}
			r := protocol.AttentionResponsibility{OriginIssueID: route.issue.IssueID, IssueID: route.issue.IssueID, RecipientKind: route.kind, RecipientRef: route.ref, AssignmentEpoch: route.issue.AssignmentEpoch, OriginAssignmentEpoch: route.issue.AssignmentEpoch}
			if err := tx.SaveAttentionResponsibility(t.Context(), route.attention.AttentionID, &r, revision, e.ids.New(protocol.PrefixRequest)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	const boardPath = "/api/v1/issues?board=true&waiting_on_me=true"
	page := func(bearer, query string) protocol.IssuePage {
		t.Helper()
		var result protocol.IssuePage
		if code := e.do("GET", boardPath+query, bearer, nil, &result); code != http.StatusOK {
			t.Fatalf("board %s returned HTTP %d", query, code)
		}
		return result
	}
	ids := func(result protocol.IssuePage) []string {
		out := []string{}
		for _, item := range result.Items {
			out = append(out, item.IssueID)
		}
		return out
	}
	if code := e.do("GET", "/api/v1/issues?waiting_on_me=true", token, nil, nil); code != http.StatusBadRequest {
		t.Fatal("board-only filter was ignored", code)
	}
	var inbox protocol.AttentionPage
	if code := e.do("GET", "/api/v1/inbox?status=open&actionable_only=true&include_archived=true", token, nil, &inbox); code != http.StatusOK || len(inbox.Items) != 5 {
		t.Fatal(code, inbox)
	}
	firstPage := page(token, "&board_status=in_progress&limit=1")
	if !slices.Equal(ids(firstPage), []string{running.IssueID}) || !firstPage.More || firstPage.Next != running.IssueID ||
		firstPage.Counts[protocol.IssueInProgress] != 2 || firstPage.Counts[protocol.IssueInReview] != 1 {
		t.Fatalf("first page must count distinct Issues across statuses: %+v", firstPage)
	}
	if next := page(token, "&board_status=in_progress&limit=1&after="+firstPage.Next); next.More || !slices.Equal(ids(next), []string{later.IssueID}) {
		t.Fatal("second page lost a match", next)
	}
	const groupedColumn = "&group_review=true&board_status=in_progress&limit=1"
	wanted := []struct {
		id     string
		status protocol.IssueWorkStatus
	}{{running.IssueID, protocol.IssueInProgress}, {review.IssueID, protocol.IssueInReview}, {later.IssueID, protocol.IssueInProgress}}
	cursor := ""
	for index, expected := range wanted {
		grouped := page(token, groupedColumn+cursor)
		if !slices.Equal(ids(grouped), []string{expected.id}) || grouped.Counts[protocol.IssueInProgress] != 3 || len(grouped.Counts) != 5 {
			t.Fatalf("page %d: grouped count or membership changed: %+v", index, grouped)
		}
		if item := grouped.Items[0]; item.EffectiveWorkStatus != expected.status || item.Board == nil || item.Board.Status != expected.status {
			t.Fatal("grouped display rewrote the persisted status", item)
		}
		if grouped.More != (index < len(wanted)-1) {
			t.Fatal("grouped continuation is incomplete", grouped)
		}
		cursor = "&after=" + grouped.Next
	}
	if code := e.do("GET", boardPath+"&group_review=true&board_status=in_review", token, nil, nil); code != http.StatusBadRequest {
		t.Fatal("grouped read exposed the removed column", code)
	}
	if got := page(token, "&board_status=in_review"); !slices.Equal(ids(got), []string{review.IssueID}) {
		t.Fatal("the personal planner action must keep its review column", got)
	}
	if got := page(token, "&board_status=backlog"); len(got.Items) != 0 {
		t.Fatal("other owner and child actions matched an unrelated card", got)
	}
	e.peer.set(netx.Identity{User: otherLogin})
	if got := page(otherSession.Token, "&board_status=backlog"); !slices.Equal(ids(got), []string{outsider.IssueID}) {
		t.Fatal("explicit owner routing did not match the other owner", got)
	}
	e.peer.set(netx.Identity{User: ownerLogin})
	for _, a := range []protocol.Attention{first, second} {
		_, response := e.op(token, e.envelope("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, &a.Revision, protocol.ConfirmAttentionPayload{Summary: "The owner completed the action"}))
		if response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
			t.Fatal(response.Operation)
		}
	}
	if got := page(token, "&board_status=in_progress"); !slices.Equal(ids(got), []string{later.IssueID}) || got.Counts[protocol.IssueInProgress] != 1 {
		t.Fatal("completed actions kept their former Issue, including a planner duplicate", got)
	}
	if got := page(token, "&group_review=true&board_status=in_progress"); !slices.Equal(ids(got), []string{review.IssueID, later.IssueID}) {
		t.Fatal("grouped refresh retained a completed action or lost a genuine review card", got)
	}
	if got := page(token, "&project=missing"); len(got.Items) != 0 || got.More || got.Counts[protocol.IssueInReview] != 0 {
		t.Fatal("project filters must apply before counting matches", got)
	}
}
