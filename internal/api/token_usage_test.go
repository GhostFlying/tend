package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"testing"
	"time"
)

func TestTokenUsageAndRolloutReadsRequireOwnerAndValidateWindows(t *testing.T) {
	e := newEnv(t)
	owner := e.login(protocol.ScopeOwner)
	agent := e.retiredAgentToken(clientID)
	for _, token := range []string{"", agent} {
		for _, path := range []string{"/api/v1/token-usage", "/api/v1/runs/missing/rollout"} {
			status := e.do("GET", path, token, nil, nil)
			if status != 401 && status != 403 {
				t.Fatalf("%s: %d", path, status)
			}
		}
	}
	var v protocol.TokenUsageView
	if status := e.do("GET", "/api/v1/token-usage?window=total", owner, nil, &v); status != http.StatusOK || v.Totals.TotalKnown != 0 || v.Timezone != "Asia/Shanghai" {
		t.Fatal(status, v)
	}
	for _, query := range []string{"window=invalid", "window=day&offset=1", "before=-1"} {
		if status := e.do("GET", "/api/v1/token-usage?"+query, owner, nil, nil); status != 400 {
			t.Fatal(query, status)
		}
	}
	if status := e.do("GET", "/api/v1/runs/missing/rollout", owner, nil, nil); status != 404 {
		t.Fatal(status)
	}
}

func TestUsageIssueOrderingHonorsAPIAndCLIQuery(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	larger := createOwnerIssueHTTP(t, e, token, "")
	recent := createOwnerIssueHTTP(t, e, token, "")
	for i, issue := range []protocol.Issue{larger, recent} {
		total := int64(500 - i*400)
		at := e.clock.Now().Add(time.Duration(i-2) * time.Hour).UnixMilli()
		sample := protocol.TokenUsageSample{Source: "test-native", RequestID: issue.IssueID, ThreadID: issue.IssueID, SessionID: issue.IssueID, Revision: 1, Settlement: "final", OccurredAt: &at, Total: &total}
		if err := e.app.RecordTokenUsage(t.Context(), "issue:"+issue.IssueID, sample); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ query, first string }{{"recent", recent.IssueID}, {"total", larger.IssueID}} {
		var view protocol.TokenUsageView
		status := e.do("GET", "/api/v1/token-usage?window=total&group_by=issue&sort="+test.query, token, nil, &view)
		if status != http.StatusOK || len(view.Issues) != 2 || view.Issues[0].ID != test.first || view.Totals.Total != 600 {
			t.Fatal(test.query, status, view)
		}
	}
}
