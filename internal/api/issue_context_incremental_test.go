package api

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueContextIncrementalHTTPIsSameReadProjection(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	path := "/api/v1/issues/" + issue.IssueID + "/context?view=incremental&limit=1"
	before := e.count("SELECT COUNT(*) FROM request_records")
	var direct protocol.IssueContextIncremental
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		direct, err = views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Limit: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var first protocol.IssueContextIncremental
	if code := e.do("GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code := e.do("GET", path, token, nil, &first); code != http.StatusOK || !reflect.DeepEqual(first, direct) {
		t.Fatal(code, first, direct)
	}
	var second protocol.IssueContextIncremental
	if code := e.do("GET", path+"&cursor="+url.QueryEscape(first.NextCursor), token, nil, &second); code != http.StatusOK || second.Kind != "delta" || len(second.Items) != 0 {
		t.Fatal(code, second)
	}
	if e.count("SELECT COUNT(*) FROM request_records") != before {
		t.Fatal("read created request record")
	}
	for _, query := range []string{"view=incremental&after=0", "view=incremental&limit=101", "after=1&after_source=2", "view=incremental&cursor=broken", "view=incremental&limit=1&limit=2"} {
		if code := e.do("GET", "/api/v1/issues/"+issue.IssueID+"/context?"+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal(query, code)
		}
	}
}
