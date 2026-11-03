package mainthread

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRecoveryReadinessPreparedWithoutSession(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	prepareArchive(t, e)
	e.m.mu.Lock()
	e.m.session, e.m.thread = nil, nil
	e.m.mu.Unlock()
	if err := e.m.RecoveryReadiness(t.Context()); err != nil {
		t.Fatalf("quiescent archive preparation blocked recovery: %v", err)
	}
	e.m.mu.Lock()
	e.m.maintenanceBlocked = true
	e.m.mu.Unlock()
	if err := e.m.RecoveryReadiness(t.Context()); err == nil {
		t.Fatal("unresolved maintenance admitted recovery")
	}
}

func TestRecoveryReadinessPreparedStillRequiresNativeReceipt(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	message := e.say("retained native work")
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{
		RequestID: e.ids.New(protocol.PrefixRequest), MessageID: message,
		ThreadID: "retained-thread", TurnID: "retained-turn", Status: store.TurnStarted,
	}); err != nil {
		t.Fatal(err)
	}
	prepareArchive(t, e)
	e.m.mu.Lock()
	e.m.session, e.m.thread = nil, nil
	e.m.mu.Unlock()
	if err := e.m.RecoveryReadiness(t.Context()); err == nil {
		t.Fatal("unread native receipt admitted recovery without a session")
	}
}
