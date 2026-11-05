package mainthread

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
	"time"
)

func TestReviewMainClaimedBeforeReadyOpenFailureReachesOwner(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	clock := testkit.NewClock()
	e.app = app.New(e.store, e.ids, clock.Now)
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Retain and process original owner input"}, app.SubmitMessage{})
	claimed, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal("fixture did not claim main input before ready", claimed, ok, err)
	}
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	opens := 0
	m := New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{Instance: "main", Log: quiet(), Open: func(context.Context) (harness.Session, error) {
		opens++
		return nil, errors.New("fixture: runtime Open fails before ready")
	}})
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := m.serve(t.Context()); err == nil {
			t.Fatal("unexpected startup success")
		}
		clock.Advance(31 * time.Second)
	}
	if err := m.serve(t.Context()); err != nil || opens != int(domain.DefaultAgentRecoveryPolicy().MaxAttempts) {
		t.Fatal("claimed pre-ready input continued automatic Open after exhaustion", opens, err)
	}
	if e.count(`SELECT COUNT(*) FROM main_turns`) != 0 {
		t.Fatal("fixture unexpectedly admitted native turn")
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		original, err := store.GetOutboxByRequest(t.Context(), tx.Q(), claimed.RequestID)
		if err != nil {
			return err
		}
		if original.Status != store.OutboxSending || original.Attempts != claimed.Attempt {
			t.Fatal("pre-ready failure released or replayed the dispatch claim", original)
		}
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open", Recipient: "owner", ActionableOnly: true})
		if err == nil && len(page.Items) != 1 {
			t.Fatalf("claimed-before-ready input lost owner fallback: owner_actions=%d open_calls=%d original_request=%s", len(page.Items), opens, claimed.RequestID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
