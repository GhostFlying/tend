package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssuePRPolicyHTTPAuthorityAndReviewRead(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	plan := issue.Plan
	plan.PRPolicy = &protocol.IssuePRPolicy{Mode: "auto", Repository: "example/project", Reason: "Integrate within the agreed Issue scope"}
	request := e.envelope("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch})
	updated := issueHTTP(t, e, token, request)
	policy := updated.Plan.PRPolicy
	if policy == nil || policy.Authority == nil || policy.Authority.RequestID != request.RequestID || policy.Authority.ScopeDigest == "" {
		t.Fatal("authenticated owner policy lost its server-recorded authority", policy)
	}
	assertOwnerStaleHTTP(t, e, token, request)
	replayed := issueDetailHTTP(t, e, token, issue.IssueID)
	if replayed.Revision != updated.Revision || replayed.Plan.PRPolicy.Authority.RequestID != request.RequestID {
		t.Fatal("stale policy input minted another grant", replayed)
	}
	before := e.count(`SELECT count(*) FROM events`)
	var page protocol.SharedDeliveryReviewPage
	route := "/api/v1/issues/" + issue.IssueID + "/delivery-reviews"
	if status := e.do("GET", route+"?delivery_id=github%3Aexample%2Fproject%2312&after=previous&limit=2", token, nil, &page); status != http.StatusOK || page.Items == nil || len(page.Items) != 0 || page.More || page.Next != "" {
		t.Fatal("empty filtered review read", status, page)
	}
	if status := e.do("GET", route, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal("review read did not require owner access", status)
	}
	for _, limit := range []string{"0", "101", "invalid"} {
		if status := e.do("GET", route+"?limit="+limit, token, nil, nil); status != http.StatusBadRequest {
			t.Fatal("invalid review page size", limit, status)
		}
	}
	if e.count(`SELECT count(*) FROM events`) != before {
		t.Fatal("review reads created business requests")
	}
}
