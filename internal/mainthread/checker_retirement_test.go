package mainthread

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRetiredCheckerNeverOpensANativeSession(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	const id = "work-item:retired-checker"
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,created_at) VALUES(?,0)`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	opens := 0
	m := New(e.app, e.m.context, e.m.tools, Config{ConversationID: id, Instance: id, Log: quiet(), Open: func(context.Context) (harness.Session, error) { opens++; return e.h.Open(t.Context()) }})
	err := m.serve(t.Context())
	if protocol.CodeOf(err) != protocol.CodeInvalidTransition || opens != 0 {
		t.Fatalf("retired checker startup: opens=%d err=%v", opens, err)
	}
	if _, err := e.app.AgentContext(t.Context(), id, "retired-request"); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("retired checker context admission: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM agent_turn_contexts WHERE request_id='retired-request'`) != 0 {
		t.Fatal("retired checker acquired a context admission")
	}
}
