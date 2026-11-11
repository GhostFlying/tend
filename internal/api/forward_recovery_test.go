package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestForwardRecoveryHTTPCommittedReplay(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	taskID := e.task("retained")
	rev := int64(1)
	env := e.envelope("control.set_mode", protocol.Target{Type: "task", ID: taskID}, &rev, protocol.SetControlModePayload{Mode: protocol.ControlPaused})
	st, first := e.op(tok, env)
	if st != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("fixture %d %+v", st, first)
	}
	app.WithForwardRecoveryStartup("fixture-recovery")(e.app)
	var current protocol.TaskDetail
	if status := e.do("GET", "/api/v1/tasks/"+taskID, tok, nil, &current); status != http.StatusOK || current.ControlMode != protocol.ControlPaused || current.ControlRevision != 2 {
		t.Fatal("committed business state was unavailable behind admission fence", status, current)
	}
	st, again := e.op(tok, env)
	if st == http.StatusOK || protocol.CodeOf(again.Error) != protocol.CodeInvalidTransition {
		t.Fatal("old trace bypassed durable receipt admission fence", st, again)
	}
	env.RequestID = e.ids.New(protocol.PrefixRequest)
	status, fresh := e.op(tok, env)
	if status == http.StatusOK || protocol.CodeOf(fresh.Error) != protocol.CodeInvalidTransition {
		t.Fatalf("new owner action crossed fence: %d %+v", status, fresh)
	}
}
