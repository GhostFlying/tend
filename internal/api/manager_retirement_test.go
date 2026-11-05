package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerRetirementOwnerBoundaryAndOriginalMissingReceipt(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	path := "/api/v1/tasks/missing/manager-retirement"
	if code := e.do("GET", path+"?run_id=missing", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal("manager retirement exposed without owner authentication", code)
	}
	for _, query := range []string{"", "?run_id=missing&limit=0", "?run_id=missing&limit=101", "?run_id=missing&limit=wrong"} {
		if code := e.do("GET", path+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal("invalid original identity or page size accepted", query, code)
		}
	}
	if code := e.do("GET", path+"?run_id=missing&limit=1", token, nil, nil); code != http.StatusNotFound {
		t.Fatal("missing original Run was not typed", code)
	}
	zero := int64(0)
	p := protocol.RetireManagerRunPayload{IssueID: "original-issue", ExpectedIssueRevision: 1, ExpectedAssignmentEpoch: 2, RunID: "missing", HostID: "original-host", LeaseID: "original-lease", ExecutionSHA256: strings.Repeat("a", 64), Reason: "Retire only the original cancelling manager"}
	env := e.envelope(protocol.OpRetireManagerRun, protocol.Target{Type: "task", ID: "missing"}, &zero, p)
	status, first := e.op(token, env)
	if status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpFailed || first.Operation.Error.Code != protocol.CodeNotFound {
		t.Fatal("missing manager lost the original request receipt", status, first)
	}
	status, replay := e.op(token, env)
	if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
		t.Fatal("missing original retirement changed under replay", status, replay)
	}
}
