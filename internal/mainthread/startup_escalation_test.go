package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
	"github.com/GhostFlying/tend/internal/views"
)

func TestMainPreReadyOpenFailureReachesOwnerWithoutStartingTurn(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	clock := testkit.NewClock()
	e.app = app.New(e.store, e.ids, clock.Now)
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Continue the original authorized goal"}, app.SubmitMessage{})
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	opens := 0
	m := New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{Instance: "main", Log: quiet(), Open: func(context.Context) (harness.Session, error) {
		opens++
		return nil, errors.New("fixture: local runtime cannot open")
	}})
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := m.serve(t.Context()); err == nil {
			t.Fatal("fixture startup unexpectedly succeeded")
		}
		clock.Advance(31 * time.Second)
	}
	if allowed, err := e.app.AgentStartupAllowed(t.Context(), "main"); err != nil || allowed {
		t.Fatal("failed main runtime remained eligible for automatic startup", allowed, err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open", Recipient: "owner", ActionableOnly: true})
		if err == nil && (len(page.Items) != 1 || page.Items[0].SourceMessageID == "") {
			t.Fatal("real pre-ready failures did not produce the original owner's action", page)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if opens != int(domain.DefaultAgentRecoveryPolicy().MaxAttempts) || e.count(`SELECT COUNT(*) FROM main_turns`) != 0 {
		t.Fatal("startup failure fabricated native execution", opens)
	}
}
