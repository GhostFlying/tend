package api

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerReconciliationOwnerRoutesAndMissingReferenceReceipt(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	path := "/api/v1/tasks/missing/manager-reconciliation"
	if code := e.do("GET", path+"?run_id=missing", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal("reconciliation evidence exposed without owner authentication", code)
	}
	if code := e.do("GET", path, token, nil, nil); code != http.StatusBadRequest {
		t.Fatal("exact original Run omitted", code)
	}
	if code := e.do("GET", path+"?run_id=missing", token, nil, nil); code != http.StatusNotFound {
		t.Fatal("missing original evidence was not typed", code)
	}
	zero := int64(0)
	p := protocol.ReconcileManagerRunPayload{RunID: "missing", LeaseID: "original", CloseRequestID: "close", CloseResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExpectedAssignmentEpoch: 1, Reason: "Reconcile the original recorded proof"}
	env := e.envelope(protocol.OpReconcileManagerRun, protocol.Target{Type: "task", ID: "missing"}, &zero, p)
	status, first := e.op(token, env)
	if status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpFailed || first.Operation.Error.Code != protocol.CodeNotFound {
		t.Fatal("missing reference lost original request receipt", status, first)
	}
	status, replay := e.op(token, env)
	if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
		t.Fatal("missing original identity was not replayed", status, replay)
	}
}
