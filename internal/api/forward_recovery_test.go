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
	st, again := e.op(tok, env)
	if st != http.StatusOK || again.Operation == nil || string(again.Operation.Result) != string(first.Operation.Result) {
		t.Fatalf("committed retry returned %d %+v instead of original receipt", st, again)
	}
	env.RequestID = e.ids.New(protocol.PrefixRequest)
	status, fresh := e.op(tok, env)
	if status == http.StatusOK || protocol.CodeOf(fresh.Error) != protocol.CodeInvalidTransition {
		t.Fatalf("new owner action crossed fence: %d %+v", status, fresh)
	}
}
