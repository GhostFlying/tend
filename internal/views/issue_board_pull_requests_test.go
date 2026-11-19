package views

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBoardPullRequestsPreferFormalThenOpenInRegistrationOrder(t *testing.T) {
	refs := []protocol.IssueDelivery{
		{DeliveryID: "closed", Provider: "github", Repository: "a/repo", Number: 1},
		{DeliveryID: "open", Provider: "github", Repository: "a/repo", Number: 2},
		{DeliveryID: "draft", Provider: "github", Repository: "b/other", Number: 2},
		{DeliveryID: "merged", Provider: "github", Repository: "a/repo", Number: 3},
		{DeliveryID: "ambiguous-open", Provider: "github", Repository: "a/repo", Number: 4},
		{DeliveryID: "error", Provider: "github", Repository: "a/repo", Number: 5},
		{DeliveryID: "alias", Provider: "github", Repository: "A/REPO", Number: 2},
		{DeliveryID: "codebase", Provider: "codebase", Repository: "a/repo", Number: 2},
	}
	facts := map[string]boardPullRequestFact{
		"closed":         {state: "closed"},
		"open":           {state: "open", draft: sql.NullBool{Valid: true}},
		"draft":          {state: "open", draft: sql.NullBool{Valid: true, Bool: true}},
		"merged":         {state: "merged", formal: true},
		"ambiguous-open": {state: "open"},
		"error":          {state: "merged", providerErr: "unreachable"},
	}
	out := boardPullRequests(refs, facts)
	var numbers []int64
	var statuses []protocol.IssueBoardPRStatus
	for _, pr := range out {
		numbers = append(numbers, pr.Number)
		statuses = append(statuses, pr.Status)
	}
	if !reflect.DeepEqual(numbers, []int64{3, 2, 2, 4, 1, 5}) ||
		!reflect.DeepEqual(statuses, []protocol.IssueBoardPRStatus{"merged", "open", "draft", "unknown", "closed", "unknown"}) {
		t.Fatal(out)
	}
}
