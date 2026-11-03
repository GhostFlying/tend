package api

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestQuestionInputsOwnerRoutesAndMissingReferenceReceipt(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	if code := e.do("GET", "/api/v1/decisions/missing/inputs", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code := e.do("GET", "/api/v1/decisions/missing/inputs", token, nil, nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	zero := int64(0)
	for _, operation := range []string{"decisions.grant_inputs", "decisions.revoke_inputs"} {
		env := e.envelope(operation, protocol.Target{Type: "decision", ID: "missing"}, &zero, protocol.QuestionInputPayload{Reason: "Read the original missing question as a typed outcome"})
		status, first := e.op(token, env)
		if status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpFailed || first.Operation.Error.Code != protocol.CodeNotFound {
			t.Fatal(status, first)
		}
		status, replay := e.op(token, env)
		if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
			t.Fatal("original missing reference receipt changed", status, replay)
		}
	}
}
