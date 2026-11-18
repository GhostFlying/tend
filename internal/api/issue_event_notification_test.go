package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueEventNotificationHTTPRequiresOriginalIssueEvent(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	other := createOwnerIssueHTTP(t, e, token, "")
	var position int64
	// Set only the original business event. Missing notification evidence should
	// reach the ordinary authenticated owner API as an empty diagnostic result.
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		position, err = tx.InsertEvent(t.Context(), store.EventRow{
			EventID: "issue-event-notification", Source: "test", ObjectType: "work_item",
			ObjectID: issue.IssueID, Kind: "work_item.delivery_changed", BodyJSON: `{}`, CreatedAt: e.clock.Now().UnixMilli(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := func(id string) string {
		return fmt.Sprintf("/api/v1/issues/%s/activity/%d/notification", id, position)
	}
	var page protocol.IssueEventNotificationPage
	if got := e.do("GET", path(issue.IssueID), token, nil, &page); got != http.StatusOK ||
		page.EventID != "issue-event-notification" || len(page.Notifications) != 0 {
		t.Fatal(got, page)
	}
	for _, tc := range []struct {
		url, token string
		status     int
	}{
		{path(issue.IssueID), "", http.StatusUnauthorized},
		{path(other.IssueID), token, http.StatusNotFound},
		{path(issue.IssueID) + "?after=-1", token, http.StatusBadRequest},
	} {
		if got := e.do("GET", tc.url, tc.token, nil, nil); got != tc.status {
			t.Fatal(tc.url, got, tc.status)
		}
	}
}
