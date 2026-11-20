package mainthread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestHostedWorkerChatRetainsOnlyProcessFragment(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "consult-model", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	release := make(chan struct{})
	type turnInput struct {
		thread *testkit.FakeThread
		turn   string
	}
	started := make(chan turnInput, 1)
	clock := testkit.NewClock()
	const partial = "observed only in hosted Worker Chat"
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, ItemType: harness.ItemAgentMessage, TurnID: turn, ItemID: "reply-item", Text: partial})
		th.Emit(harness.Event{Kind: harness.EventItemStarted, ItemType: "reasoning", TurnID: turn, ItemID: "following-event", Raw: json.RawMessage(`{"following_event":true}`)})
		started <- turnInput{th, turn}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "interrupted"
	}
	e := newEnv(t, h)
	e.stop()
	chat := createConsultationFixture(t, e)
	configure := func(action string, revision int64, options protocol.ConfigureConcurrencyPayload) {
		t.Helper()
		cmd := app.ConfigureConcurrency{Action: action}
		body, _ := json.Marshal(options)
		env := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: cmd.Operation(),
			Target: protocol.Target{Type: "concurrency", ID: "hosted-worker"}, Payload: body, ExpectedControlRevision: &revision}
		if op, err := e.app.Execute(ctx, owner, env, cmd); err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	configure("create", 0, protocol.ConfigureConcurrencyPayload{Name: "Hosted Worker", Min: 1, Initial: 1, Max: 2})
	configure("bind", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "hosted", RuntimeID: "runtime"})
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	service := tools.New(e.app, e.store, cb, quiet())
	pool := &BackgroundPool{App: e.app, New: func(id string) (*Manager, error) {
		return New(e.app, cb, service, Config{
			Instance: "hosted/" + id, ConversationID: id,
			Thread: ThreadConfig(t.TempDir(), "unused"), Open: h.Open,
			OpenNamed: func(ctx context.Context, selection hostedconfig.Selection) (harness.Session, error) {
				if selection.RunnerManaged {
					t.Error("expected the actual server-hosted Chat path")
				}
				return h.Open(ctx)
			},
			RestartDelay: 10 * time.Millisecond, Log: quiet(), Now: clock.Now,
		}), nil
	}}
	stopped := make(chan struct{})
	go func() { defer close(stopped); pool.Run(ctx) }()
	defer func() { cancel(); <-stopped }()
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: chat.ID, Body: "Original input"}, app.SubmitMessage{})
	eventually(t, "non-text native event after the fragment was collected", func() bool {
		return e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE conversation_id=? AND item_id='following-event'`, chat.ID) == 1
	})
	readProcess := func() bool {
		t.Helper()
		var found bool
		err := e.store.Read(ctx, func(tx *store.ReadTx) error {
			p, err := store.AgentRollout(ctx, tx.Q(), chat.ID, "", 0)
			for _, record := range p.Records {
				found = found || strings.Contains(record.Data, partial)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	select {
	case state := <-started:
		live := e.app.AgentProcessText(chat.ID, state.thread.ID(), state.turn)
		if len(live) != 1 || live[0].Text != partial || readProcess() {
			t.Fatal("live Worker text should be readable directly before its collected boundary")
		}
		var initial int64
		if err := e.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT last_seen FROM concurrency_executions WHERE state='running' AND json_extract(source_json,'$.conversation_id')=?`, chat.ID).Scan(&initial)
		}); err != nil {
			t.Fatal(err)
		}
		clock.Advance(6 * time.Second)
		state.thread.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, ItemType: harness.ItemAgentMessage, TurnID: state.turn, ItemID: "reply-item", Text: " tail"})
		eventually(t, "delta-only native activity updates the original running execution", func() bool {
			return e.count(`SELECT COUNT(*) FROM concurrency_executions WHERE state='running' AND json_extract(source_json,'$.conversation_id')=? AND last_seen>?`, chat.ID, initial) == 1
		})
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(release)
	eventually(t, "original turn is interrupted", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id WHERE h.conversation_id=? AND t.status='interrupted'`, chat.ID) == 1
	})
	visible := e.count(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id=? AND body=?`, chat.ID, partial)
	collected := readProcess()
	if visible != 0 || !collected || len(e.app.AgentProcessText(chat.ID, "", "")) != 0 {
		t.Fatal("the hosted Worker Chat dropped observed text from its collected process while correctly omitting it from the ordinary surface")
	}
}
