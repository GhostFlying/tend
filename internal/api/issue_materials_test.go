package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueMaterialsOwnerRoutePreservesAuthenticationAndMissingSource(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	if code := e.do("GET", "/api/v1/issues/missing/materials", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code := e.do("GET", "/api/v1/issues/missing/materials", token, nil, nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	issue := createOwnerIssueHTTP(t, e, token, "")
	for _, query := range []string{"limit=0", "limit=101", "limit=invalid"} {
		if code := e.do("GET", "/api/v1/issues/"+issue.IssueID+"/materials?"+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal("invalid page size accepted", query, code)
		}
	}
}
