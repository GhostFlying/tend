package mainthread

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
)

type continuedThread struct {
	harness.Thread
	interrupted string
}

func (t *continuedThread) ID() string { return "thread" }
func (t *continuedThread) Interrupt(_ context.Context, id string) error {
	t.interrupted = id
	return nil
}
func TestDelegatedContinuedTurnInterrupt(t *testing.T) {
	th := &continuedThread{}
	m := &Manager{cfg: Config{InterruptTimeout: time.Second}, thread: th, active: &turn{turnID: "root", messageID: "source", sourceMessageID: "source", sources: []string{"source"}}}
	if err := m.handle(t.Context(), th, harness.Event{Kind: harness.EventTurnStarted, TurnID: "continuation", CoordinatorRootTurn: "root"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(app.InterruptTurn{MessageID: "source"})
	m.Control().Deliver(t.Context(), app.Delivery{Payload: raw})
	if th.interrupted != "continuation" {
		t.Fatalf("interrupted %q; live native turn is continuation", th.interrupted)
	}
}
func TestDelegatedContinuedReplyAttribution(t *testing.T) {
	m := &Manager{active: &turn{turnID: "root", messageID: "source", sourceMessageID: "source", sources: []string{"source"}}}
	drafts := replyDrafts{}
	ev := harness.Event{Kind: harness.EventItemStarted, ItemType: harness.ItemAgentMessage, TurnID: "continuation", ItemID: "reply", CoordinatorRootTurn: "root"}
	drafts.begin(ev, m)
	if got := drafts[replyKey{ev.TurnID, ev.ItemID}]; got.replyTo != "source" || len(got.sources) != 1 {
		t.Fatalf("reply to=%q sources=%v; continuation lost original source", got.replyTo, got.sources)
	}
}
