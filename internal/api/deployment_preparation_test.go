package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueDeploymentPreparationOwnerAPIAndNativeCLIShareReadOnlyProjection(t *testing.T) {
	e := newEnv(t)
	e.configure()
	owner := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, owner, "")
	c := newCapabilityCLI(t, e)
	counts := func() [3]int64 {
		t.Helper()
		var v [3]int64
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM request_records), (SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM outbox)`).Scan(&v[0], &v[1], &v[2])
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := counts()
	want, err := e.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	if want.Preparation == nil || !reflect.DeepEqual(want.Preparation.HostIDs, []string{"host-a"}) || want.Preparation.Complete || len(want.Preparation.Missing) == 0 {
		t.Fatalf("fixture must expose fixed targets and preserve unavailable installation facts: %+v", want)
	}
	var got protocol.ObservedDeployment
	if status := e.do(http.MethodGet, "/api/v1/issues/"+issue.IssueID+"/deployment-preparation", owner, nil, &got); status != http.StatusOK {
		t.Fatalf("owner read: HTTP %d", status)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("API differs from source projection: got=%+v want=%+v", got, want)
	}
	for _, format := range []string{"--json", "--json=false"} {
		var cliValue protocol.ObservedDeployment
		raw := c.run("issue", "deployment-preparation", issue.IssueID, format)
		if err := json.Unmarshal(raw, &cliValue); err != nil || !reflect.DeepEqual(cliValue, want) {
			t.Fatalf("native CLI lost complete read facts: %s, %v", raw, err)
		}
	}
	if after := counts(); after != before {
		t.Fatalf("reads created mutation/notification work: before=%v after=%v", before, after)
	}
}

func TestIssueDeploymentPreparationOwnerAuthAndInputs(t *testing.T) {
	e := newEnv(t)
	e.configure()
	owner := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, owner, "")
	path := "/api/v1/issues/" + issue.IssueID + "/deployment-preparation"
	for _, token := range []string{"", "not-a-token"} {
		if status := e.do(http.MethodGet, path, token, nil, nil); status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated read: HTTP %d", status)
		}
	}
	for _, query := range []string{"?host_id=unrelated", "?provider=github", "?issue_id=unrelated"} {
		var response protocol.APIResponse
		if status := e.do(http.MethodGet, path+query, owner, nil, &response); status != http.StatusBadRequest || response.Error == nil || response.Error.Code != protocol.CodeInvalidRequest {
			t.Fatalf("unexpected selector accepted: HTTP %d %+v", status, response)
		}
	}
	var response protocol.APIResponse
	if status := e.do(http.MethodGet, "/api/v1/issues/missing/deployment-preparation", owner, nil, &response); status != http.StatusNotFound || response.Error == nil || response.Error.Code != protocol.CodeNotFound {
		t.Fatalf("missing Issue: HTTP %d %+v", status, response)
	}
}
