package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type replayObservedThread struct {
	id  string
	ack func(harness.Event, []int64) error
}

func (t replayObservedThread) ID() string { return t.id }
func (t replayObservedThread) AcknowledgeReplyPrefix(_ context.Context, event harness.Event, consumed []int64) error {
	return t.ack(event, consumed)
}

func retainedReplyScope() *app.ManagerReplyReplay {
	scope := &app.ManagerReplyReplay{RunID: "original-run", ThreadID: "original-thread"}
	for i, text := range []string{"original", " retained", " reply"} {
		scope.Events = append(scope.Events, app.ManagerReplayEvent{RunID: scope.RunID, Sequence: int64(17 + i), Body: json.RawMessage(fmt.Sprintf(`{"thread_id":"original-thread","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":"original-item","Text":%q}}`, text))})
	}
	return scope
}

func TestDatabaseReplyReplayConsumesEntireBoundaryBeforeDurableAcknowledgement(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	scope := retainedReplyScope()
	acks := 0
	thread := replayObservedThread{id: scope.ThreadID, ack: func(event harness.Event, consumed []int64) error {
		acks++
		if event.Text != "original retained reply" || event.CoordinatorSequence != 19 || !slices.Equal(consumed, []int64{17, 18, 19}) {
			t.Fatal("acknowledgement lost its actually consumed original prefix", event, consumed)
		}
		items := assistantItems(t, e)
		if len(items) != 1 || items[0].Body != event.Text || items[0].AssistantState != protocol.AssistantInterrupted {
			t.Fatal("database prefix acknowledged before terminal text was durable", items)
		}
		return nil
	}}
	if err := e.m.consumeDatabaseReplies(t.Context(), scope, thread); err != nil || acks != 1 {
		t.Fatal("fixed original prefix was not durably consumed", acks, err)
	}
}

func TestDatabaseReplyReplayRejectsIncompleteConsumption(t *testing.T) {
	for _, failure := range []string{"identity", "decode", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			scope := retainedReplyScope()
			ctx := t.Context()
			switch failure {
			case "identity":
				scope.Events[1].RunID = "another-run"
			case "decode":
				scope.Events[1].Body = json.RawMessage(`{`)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			thread := replayObservedThread{id: scope.ThreadID, ack: func(harness.Event, []int64) error {
				t.Fatal("incomplete original snapshot was acknowledged")
				return nil
			}}
			if err := e.m.consumeDatabaseReplies(ctx, scope, thread); err == nil {
				t.Fatal("invalid read boundary was consumed")
			}
			if items := assistantItems(t, e); len(items) != 0 {
				t.Fatal("partial failed consumption was published as the full prefix", items)
			}
		})
	}
}

func TestDatabaseReplayUsesOnlyOriginalNamedBindingWithoutOpeningHarness(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.cfg.Open = func(context.Context) (harness.Session, error) {
		t.Fatal("database replay opened a native harness")
		return nil, nil
	}
	e.m.cfg.OpenNamed = func(context.Context, hostedconfig.Selection) (harness.Session, error) {
		t.Fatal("database replay queried models or opened a runner session")
		return nil, nil
	}
	selection := hostedconfig.Selection{RunnerManaged: true, RunnerRunID: "original-run", ReceiptOnly: true}
	e.m.named = &selection
	if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), "original-thread"); err != nil {
		t.Fatal(err)
	}
	scope := &app.ManagerReplyReplay{RunID: "original-run", ThreadID: "another-thread", Selection: selection}
	if err := e.m.replayDatabaseReplies(t.Context(), scope); err == nil {
		t.Fatal("database replay substituted another thread")
	}
	scope.ThreadID = "original-thread"
	if err := e.m.replayDatabaseReplies(t.Context(), scope); err != nil {
		t.Fatal("database-only empty read boundary required a live session", err)
	}
}

func TestDatabaseReplayManagerCannotAcknowledgeLiveInterrupt(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	const conversation = "work-item:original"
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,created_at) VALUES(?,0)`, conversation); err != nil {
			return err
		}
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{ConversationID: conversation, MessageID: "original-message", Author: "owner", Body: "original input"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pool := BackgroundPool{App: e.app, active: map[string]*Manager{conversation: e.m}, database: map[string]bool{conversation: true}}
	if result := pool.Deliver(t.Context(), app.Delivery{Payload: []byte(`{"message_id":"original-message"}`)}); result.Outcome != app.Unknown {
		t.Fatal("database-only receipt reader asserted a live native interrupt", result)
	}
	if drained, err := pool.UpgradeDrainStatus(t.Context()); err != nil || drained {
		t.Fatal("database writer disappeared from maintenance drain", drained, err)
	}
}
