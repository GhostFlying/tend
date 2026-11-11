package tools

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestSharedReviewReadIsAdvertisedWithoutMainWriteAuthority(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		names := map[string]bool{}
		for _, s := range NamedSpecs(purpose) {
			names[s.Name] = true
		}
		if names["issues_delivery_reviews_read"] != (purpose != agentconfig.Consultation) {
			t.Fatal("shared review read unavailable", purpose)
		}
		if purpose == agentconfig.Main && names["issues_review_delivery"] {
			t.Fatal("Main acquired Issue review write authority")
		}
	}
}

func TestSharedReviewReadScopeFiltersAndPagination(t *testing.T) {
	e := newEnv(t)
	counter := 0
	ids := testkit.NewIDGen(testkit.NewClock(), 91)
	create := func(parent string) protocol.Issue {
		counter++
		raw, _ := json.Marshal(protocol.CreateIssuePayload{ParentIssueID: parent, Plan: protocol.IssuePlan{Title: "Shared delivery", Project: "default", Intent: "Review exact PR"}})
		op, err := e.svc.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "review-read-test"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "issues.create", Target: protocol.Target{Type: "issue", ID: ids.New(protocol.PrefixWorkItem)}, Payload: raw}, app.CanonicalOwnerIssueCommand{Action: "create"})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		var issue protocol.Issue
		if err := json.Unmarshal(op.Result, &issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	root := create("")
	child := create(root.IssueID)
	other := create("")
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for n, id := range []string{"review-a", "review-b"} {
			r := protocol.SharedDeliveryReview{ReviewID: id, Provider: "github", Repository: "example/project", Number: int64(n + 1), Revision: 1, Status: "pending", Links: []protocol.SharedDeliveryReviewLink{{IssueID: child.IssueID, DeliveryID: fmt.Sprintf("pr-%d", n+1)}}}
			if err := tx.PutSharedDeliveryReview(t.Context(), r, fmt.Sprintf("seed-%d", n)); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO shared_delivery_review_links(review_id,issue_id,delivery_id,task_id) VALUES(?,?,?,'')`, id, child.IssueID, r.Links[0].DeliveryID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call := func(purpose agentconfig.Purpose, scope, issue, delivery, after string, limit int) answerView {
		raw, _ := json.Marshal(map[string]any{"issue_id": issue, "delivery_id": delivery, "after": after, "limit": limit})
		res, handled := e.svc.canonicalIssueRead(t.Context(), Call{Tool: "issues_delivery_reviews_read", Arguments: raw}, store.HostedExecution{IssueID: scope, Selection: hostedconfig.Selection{Purpose: purpose}})
		if !handled {
			t.Fatal("read tool was not routed")
		}
		var answer answerView
		if err := json.Unmarshal([]byte(res.Text), &answer); err != nil {
			t.Fatal(err, res)
		}
		return answer
	}
	first := call(agentconfig.IssueManager, root.IssueID, child.IssueID, "", "", 1)
	var page protocol.SharedDeliveryReviewPage
	if !first.OK || json.Unmarshal(first.Result, &page) != nil || len(page.Items) != 1 || page.Items[0].ReviewID != "review-a" || !page.More || page.Next != "review-a" {
		t.Fatal(first, page)
	}
	second := call(agentconfig.IssueManager, root.IssueID, child.IssueID, "", page.Next, 1)
	if !second.OK || json.Unmarshal(second.Result, &page) != nil || len(page.Items) != 1 || page.Items[0].ReviewID != "review-b" || page.More {
		t.Fatal(second, page)
	}
	filtered := call(agentconfig.Main, "", child.IssueID, "pr-2", "", 100)
	if !filtered.OK || json.Unmarshal(filtered.Result, &page) != nil || len(page.Items) != 1 || page.Items[0].ReviewID != "review-b" {
		t.Fatal(filtered, page)
	}
	for _, answer := range []answerView{call(agentconfig.IssueManager, root.IssueID, other.IssueID, "", "", 1), call(agentconfig.Consultation, root.IssueID, child.IssueID, "", "", 1)} {
		if answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeForbidden {
			t.Fatal("expanded review scope", answer)
		}
	}
}
