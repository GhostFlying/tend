package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

type steerSession struct {
	harness.Session
	steer func(context.Context, harness.Thread, string, harness.TurnInput) error
}

func (s steerSession) StartThread(ctx context.Context, c harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.StartThread(ctx, c)
	if err != nil {
		return nil, err
	}
	return &steerThread{th, s.steer}, nil
}
func (s steerSession) ResumeThread(ctx context.Context, id string, c harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.ResumeThread(ctx, id, c)
	if err != nil {
		return nil, err
	}
	return &steerThread{th, s.steer}, nil
}

type steerThread struct {
	harness.Thread
	steer func(context.Context, harness.Thread, string, harness.TurnInput) error
}

func (s steerThread) SteerTurn(ctx context.Context, id string, in harness.TurnInput) error {
	return s.steer(ctx, s.Thread, id, in)
}
func withSteer(h *testkit.Harness, f func(context.Context, harness.Thread, string, harness.TurnInput) error) func(*Config) {
	return func(c *Config) {
		c.Open = func(ctx context.Context) (harness.Session, error) {
			s, err := h.Open(ctx)
			return steerSession{s, f}, err
		}
	}
}

func TestToolDisplayUsesFrozenOwnerSourceRegardlessOfCallbackOrder(t *testing.T) {
	for _, name := range []string{"item first", "callback first", "concurrent"} {
		itemFirst := name == "item first"
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			original := e.say("original action")
			d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
			if err != nil || !ok {
				t.Fatal(d, ok, err)
			}
			session, err := e.h.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			th, err := session.StartThread(t.Context(), harness.ThreadConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.app.RegisterAgentThread(t.Context(), "main", th.ID()); err != nil {
				t.Fatal(err)
			}
			if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: original, ThreadID: th.ID(), TurnID: "turn", Status: store.TurnStarted}); err != nil {
				t.Fatal(err)
			}
			latest := e.say("status question")
			sd, ok, err := e.app.ClaimMainSteer(t.Context(), d.RequestID, th.ID(), "turn")
			if err != nil || !ok {
				t.Fatal(sd, ok, err)
			}
			steer, _, err := e.app.MainSteer(t.Context(), sd.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			steer.InputText = "status-native"
			if err := e.app.UpdateMainSteer(t.Context(), steer); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := e.app.ConsumeMainSteer(t.Context(), th.ID(), "turn", steer.InputText, "user-status"); err != nil || !ok {
				t.Fatal(ok, err)
			}
			e.m.mu.Lock()
			e.m.thread = th
			e.m.active = &turn{turnID: "turn", requestID: d.RequestID, messageID: original, sourceMessageID: latest, sources: []string{original, latest}}
			e.m.mu.Unlock()
			ev := harness.Event{Kind: harness.EventItemStarted, TurnID: "turn", ItemID: "call", Tool: &harness.ToolUse{Name: "tasks_list", Input: `{}`, Status: protocol.ToolRunning}}
			if itemFirst {
				if err := e.m.recordToolUse(t.Context(), th, ev); err != nil {
					t.Fatal(err)
				}
			}
			args, _ := json.Marshal(map[string]any{"source_message_id": original})
			call := harness.ToolCall{ThreadID: th.ID(), TurnID: "turn", CallID: "call", Tool: "tasks_list", Arguments: args, UserText: steer.InputText, UserItemID: "user-status"}
			var result harness.ToolResult
			if name == "concurrent" {
				results := make(chan harness.ToolResult, 1)
				recorded := make(chan error, 1)
				go func() { results <- e.m.onToolCall(t.Context(), call) }()
				go func() { recorded <- e.m.recordToolUse(t.Context(), th, ev) }()
				result = <-results
				if err := <-recorded; err != nil {
					t.Fatal(err)
				}
			} else {
				result = e.m.onToolCall(t.Context(), call)
			}
			if !result.Success {
				t.Fatal(result)
			}
			if name == "callback first" {
				if err := e.m.recordToolUse(t.Context(), th, ev); err != nil {
					t.Fatal(err)
				}
			}
			ev.Kind = harness.EventItemCompleted
			ev.Tool.Status = protocol.ToolCompleted
			ev.Tool.Output = result.Text
			if err := e.m.recordToolUse(t.Context(), th, ev); err != nil {
				t.Fatal(err)
			}
			replay := harness.Event{Kind: harness.EventItemStarted, TurnID: "turn", ItemID: "call", Tool: &harness.ToolUse{Name: "tasks_list", Input: `{}`, Status: protocol.ToolRunning}}
			if err := e.m.recordToolUse(t.Context(), th, replay); err != nil {
				t.Fatal("replayed start conflicted after source correction", err)
			}
			if e.count(`SELECT COUNT(*) FROM conversation_tool_uses u JOIN conversation_messages m ON m.message_id=u.message_id WHERE u.item_id='call' AND m.reply_to=?`, original) != 1 {
				t.Fatal("tool display did not follow selected action source")
			}
			if e.count(`SELECT COUNT(*) FROM conversation_tool_uses u JOIN conversation_messages m ON m.message_id=u.message_id WHERE u.item_id='call' AND m.reply_to=?`, latest) != 0 {
				t.Fatal("tool display remained attached to status question")
			}
		})
	}
}

func TestSteerReceiptsOrderReplyToAndFrozenTools(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		started <- th
		<-release
		return "completed"
	}
	inputs := make(chan harness.TurnInput, 4)
	e := newEnv(t, h, withSteer(h, func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
		inputs <- in
		return nil
	}))
	first := e.say("retain ORIGINAL_GOAL")
	th := <-started
	var turnID string
	eventually(t, "started", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		if e.m.active != nil {
			turnID = e.m.active.turnID
		}
		return turnID != ""
	})
	th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turnID, ItemID: "before", Text: "initial"})
	args, _ := json.Marshal(map[string]any{"title": "one planning effect", "spec": spec, "commit_seq": 1})
	call := harness.ToolCall{ThreadID: th.ID(), TurnID: turnID, CallID: "planning-call", Tool: "tasks_create", Arguments: args}
	if result := e.m.onToolCall(t.Context(), call); !result.Success {
		t.Fatal(result.Text)
	}
	second := e.say("clarify NEW_REQUIREMENT")
	var in harness.TurnInput
	select {
	case in = <-inputs:
	case <-time.After(time.Second):
		t.Fatal("no steer")
	}
	eventually(t, "accepted", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted'`, second) == 1
	})
	if len(businessStarts(h)) != 1 || !strings.Contains(in.Text, second) {
		t.Fatal("new turn or missing source")
	}
	// Receipt for another turn cannot consume this input.
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: "foreign", ItemID: "wrong", ItemType: harness.ItemUserMessage, Text: in.Text})
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: "user2", ItemType: harness.ItemUserMessage, Text: in.Text})
	eventually(t, "consumed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed' AND native_item_id='user2'`, second) == 1
	})
	call.UserText, call.UserItemID = in.Text, "user2"
	if result := e.m.onToolCall(t.Context(), call); !result.Success {
		t.Fatal("replayed tool failed: ", result.Text)
	}
	if e.count(`SELECT COUNT(*) FROM tasks`) != 1 || e.count(`SELECT COUNT(*) FROM conversation_bindings WHERE message_id=? AND object_type='task'`, first) != 1 {
		t.Fatal("steer repeated or rebound a committed planning write")
	}
	// An item that already began keeps its original reply reference.
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: "before", ItemType: harness.ItemAgentMessage, Text: "initial complete"})
	third := e.say("status question, preserve goal")
	select {
	case in = <-inputs:
	case <-time.After(time.Second):
		t.Fatal("second steer missing")
	}
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: "user3", ItemType: harness.ItemUserMessage, Text: in.Text})
	eventually(t, "third consumed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed'`, third) == 1
	})
	// Review tools keep the original logical execution even when native source
	// attribution points at the newly consumed owner message.
	read := harness.ToolCall{ThreadID: th.ID(), TurnID: turnID, CallID: "review-after-steer", Tool: "conversation_turn_read", Arguments: json.RawMessage(`{}`), UserText: in.Text, UserItemID: "user3"}
	res := e.m.onToolCall(t.Context(), read)
	var reviewed struct {
		Result protocol.LogicalTurnState `json:"result"`
	}
	if !res.Success || json.Unmarshal([]byte(res.Text), &reviewed) != nil {
		t.Fatal("steered source lost logical execution: ", res.Text)
	}
	for _, source := range []string{first, second, third} {
		found := false
		for _, message := range reviewed.Result.Messages {
			found = found || message.MessageID == source
		}
		if !found {
			t.Fatalf("consumed owner source %s omitted from review", source)
		}
	}
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: "after", ItemType: harness.ItemAgentMessage, Text: "ORIGINAL_GOAL NEW_REQUIREMENT status"})
	eventually(t, "reply persisted", func() bool { return e.count(`SELECT COUNT(*) FROM conversation_reply_sources`) == 3 })
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='initial complete' AND reply_to=?`, first) != 1 {
		t.Fatal("stream source drift")
	}
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='ORIGINAL_GOAL NEW_REQUIREMENT status' AND reply_to=?`, third) != 1 {
		t.Fatal("wrong final reply")
	}
	// Native replay keeps the original tool source even after a new owner input.
	m, r, err := e.app.BindMainToolSource(t.Context(), "native-call", first, "first-request", store.ToolUseRow{})
	if err != nil || m != first || r != "first-request" {
		t.Fatal(err)
	}
	m, r, err = e.app.BindMainToolSource(t.Context(), "native-call", third, "third-request", store.ToolUseRow{})
	if err != nil || m != first || r != "first-request" {
		t.Fatal("tool source changed on replay")
	}
	th.Emit(harness.Event{Kind: harness.EventTurnCompleted, TurnID: turnID, TurnStatus: "completed"})
	eventually(t, "both replied", func() bool { return e.count(`SELECT COUNT(*) FROM main_steers WHERE status='completed'`) == 2 })
	if len(businessStarts(h)) != 1 {
		t.Fatal("steer duplicated as next turn")
	}
	// A late native replay must retain the sources of the persisted item,
	// even after the active delivery has cleared its in-memory focus.
	eventually(t, "active turn cleared", func() bool { e.m.mu.Lock(); defer e.m.mu.Unlock(); return e.m.active == nil })
	th.Say(turnID, "after", "ORIGINAL_GOAL NEW_REQUIREMENT status")
	time.Sleep(100 * time.Millisecond)
	if h.Sessions() != 1 || e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='ORIGINAL_GOAL NEW_REQUIREMENT status'`) != 1 {
		t.Fatal("late reply replay changed sources or restarted the session")
	}
	_ = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		b, err := views.TranscriptBlock(t.Context(), tx.Q(), second, 0, 10)
		if err != nil || len(b.Items) != 1 || len(b.Items[0].ReplySources) != 3 {
			t.Errorf("steer reply block %+v err=%v", b, err)
		}
		return err
	})
}

func TestFault_SteerRejectedUnknownAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		expected string
		nextTurn bool
	}{{"rejected", harness.ErrRejected, "waiting", true}, {"unknown", errors.New("connection lost"), "unknown", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := testkit.NewHarness()
			release := make(chan struct{})
			defer close(release)
			started := make(chan struct{}, 2)
			h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
				started <- struct{}{}
				<-release
				return "completed"
			}
			var calls atomic.Int32
			e := newEnv(t, h, withSteer(h, func(context.Context, harness.Thread, string, harness.TurnInput) error { calls.Add(1); return tc.err }))
			e.say("original")
			<-started
			second := e.say("new owner input")
			eventually(t, tc.expected, func() bool {
				return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status=?`, second, tc.expected) == 1
			})
			var d app.Delivery
			_ = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var id string
				err := tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM main_steers WHERE message_id=?`, second).Scan(&id)
				d.RequestID = id
				return err
			})
			got := e.m.Reconcile(t.Context(), d)
			if tc.nextTurn && got.Outcome != app.Retry || !tc.nextTurn && got.Outcome != app.Unknown {
				t.Fatalf("reconcile=%+v", got)
			}
			e.stop()
			e.start()
			if tc.nextTurn {
				eventually(t, "queued retry starts after restart", func() bool { return len(businessStarts(h)) == 2 })
			} else {
				time.Sleep(150 * time.Millisecond)
				if len(businessStarts(h)) != 1 || calls.Load() != 1 {
					t.Fatal("unknown was replayed")
				}
				_ = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
					row, err := store.GetConversationItem(t.Context(), tx.Q(), second)
					if err != nil {
						return err
					}
					it := protocol.ConversationItem{MessageID: second, Author: "owner", Body: row.Body}
					if err := store.HideUndeliveredOwner(t.Context(), tx.Q(), &it); err != nil {
						return err
					}
					if it.Body == row.Body {
						t.Error("unknown input leaked through model context")
					}
					return nil
				})
			}
		})
	}
}

func TestFault_SteerAcceptedWithoutConsumptionIsUnknown(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return "completed"
	}
	e := newEnv(t, h, withSteer(h, func(context.Context, harness.Thread, string, harness.TurnInput) error { return nil }))
	e.say("original")
	th := <-started
	second := e.say("pending steer")
	eventually(t, "acceptance", func() bool { return e.count(`SELECT COUNT(*) FROM main_steers WHERE status='accepted'`) == 1 })
	e.m.mu.Lock()
	id := e.m.active.turnID
	e.m.mu.Unlock()
	th.Emit(harness.Event{Kind: harness.EventTurnCompleted, TurnID: id, TurnStatus: "completed"})
	eventually(t, "unknown consumption", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='unknown'`, second) == 1
	})
	if len(businessStarts(h)) != 1 {
		t.Fatal("accepted input resent")
	}
}

func TestSteerDecisionChoiceRetainsActualOwnerAnswerSource(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return "completed"
	}
	inputs := make(chan harness.TurnInput, 1)
	e := newEnv(t, h, withSteer(h, func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
		inputs <- in
		return nil
	}))
	first := e.say("original goal")
	th := <-started
	var turnID string
	eventually(t, "original turn", func() bool { e.m.mu.Lock(); defer e.m.mu.Unlock(); turnID = e.m.active.turnID; return turnID != "" })
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: th.ID()}
	var decision struct {
		ID string `json:"decision_id"`
	}
	_ = json.Unmarshal(e.exec(principal, "decisions.open", protocol.Target{Type: "decision"}, map[string]any{"question": "Which scope?", "options": []string{"keep scope", "stop scope"}, "source_message_id": first}, app.OpenDecision{}), &decision)
	var answer struct {
		ID string `json:"message_id"`
	}
	choice := protocol.DecisionChoice{DecisionID: decision.ID, Version: 1, Option: "keep scope"}
	_ = json.Unmarshal(e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "keep scope", ReplyTo: first, Decision: &choice}, app.SubmitMessage{}), &answer)
	in := <-inputs
	if !strings.Contains(in.Text, "reply to "+first) || !strings.Contains(in.Text, "choosing option \"keep scope\"") {
		t.Fatal("native input lost reply/choice")
	}
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: "answer-user", ItemType: harness.ItemUserMessage, Text: in.Text})
	eventually(t, "answer consumed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed'`, answer.ID) == 1
	})
	status := e.say("status question; preserve the previous decision answer")
	statusInput := <-inputs
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: "status-user", ItemType: harness.ItemUserMessage, Text: statusInput.Text})
	eventually(t, "status consumed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed'`, status) == 1
	})
	args, _ := json.Marshal(map[string]any{"decision_id": decision.ID, "version": 1, "option": "keep scope", "source_message_id": answer.ID})
	call := harness.ToolCall{ThreadID: th.ID(), TurnID: turnID, CallID: "resolve-answer", Tool: "decisions_resolve", Arguments: args, UserText: statusInput.Text, UserItemID: "status-user"}
	if res := e.m.onToolCall(t.Context(), call); !res.Success {
		t.Fatal(res.Text)
	}
	if res := e.m.onToolCall(t.Context(), call); !res.Success {
		t.Fatal(res.Text)
	}
	if e.count(`SELECT COUNT(*) FROM decisions WHERE decision_id=? AND status='answered' AND answered_message_id=?`, decision.ID, answer.ID) != 1 {
		t.Fatal("answer source not persisted")
	}
	if e.count(`SELECT COUNT(*) FROM events WHERE kind='decision.answered' AND object_id=?`, decision.ID) != 1 {
		t.Fatal("answer parsed twice")
	}
}

func TestSteerToolSnapshotCannotAdvanceEarlierReplyFocus(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return "completed"
	}
	inputs := make(chan harness.TurnInput, 2)
	e := newEnv(t, h, withSteer(h, func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
		inputs <- in
		return nil
	}))
	first := e.say("original")
	th := <-started
	var id string
	eventually(t, "turn", func() bool { e.m.mu.Lock(); defer e.m.mu.Unlock(); id = e.m.active.turnID; return id != "" })
	second := e.say("second")
	third := e.say("third")
	in2, in3 := <-inputs, <-inputs
	// The native request reader sees user3 before serve drains user2. It
	// records user3 and binds this tool, but cannot change earlier replies.
	result := e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: id, CallID: "after-user3", Tool: "repos_list", Arguments: json.RawMessage(`{}`), UserText: in3.Text, UserItemID: "user3"})
	if !result.Success {
		t.Fatal(result.Text)
	}
	if focus, _ := e.m.replyContext(id); focus != first {
		t.Fatal("callback advanced event-loop reply focus")
	}
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: id, ItemID: "user2", ItemType: harness.ItemUserMessage, Text: in2.Text})
	th.Say(id, "reply2", "SECOND_ONLY")
	th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: id, ItemID: "user3", ItemType: harness.ItemUserMessage, Text: in3.Text})
	th.Say(id, "reply3", "THIRD")
	eventually(t, "ordered replies", func() bool {
		return e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body IN ('SECOND_ONLY','THIRD')`) == 2
	})
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='SECOND_ONLY' AND reply_to=?`, second) != 1 || e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='THIRD' AND reply_to=?`, third) != 1 {
		t.Fatal("reply attributed to future input")
	}
	if e.count(`SELECT COUNT(*) FROM conversation_reply_sources s JOIN conversation_messages m ON m.message_id=s.reply_id WHERE m.body='SECOND_ONLY' AND s.source_id=?`, third) != 0 {
		t.Fatal("earlier reply claims future owner source")
	}
}

func TestSteerMessageInterruptsItsOriginalTurn(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		name := "accepted"
		if consumed {
			name = "consumed"
		}
		t.Run(name, func(t *testing.T) {
			h := testkit.NewHarness()
			started := make(chan *testkit.FakeThread, 1)
			release := make(chan struct{})
			defer close(release)
			h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
				started <- th
				<-release
				return ""
			}
			inputs := make(chan harness.TurnInput, 1)
			e := newEnv(t, h, withSteer(h, func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
				inputs <- in
				return nil
			}))
			first := e.say("original")
			th := <-started
			second := e.say("steer")
			in := <-inputs
			e.m.mu.Lock()
			id := e.m.active.turnID
			e.m.mu.Unlock()
			eventually(t, "accepted", func() bool {
				return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted'`, second) == 1
			})
			expected := "unknown"
			if consumed {
				th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: id, ItemID: "steer-user", ItemType: harness.ItemUserMessage, Text: in.Text})
				eventually(t, "consumed", func() bool {
					return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed'`, second) == 1
				})
				expected = "interrupted"
			}
			e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: second}, protocol.InterruptReplyPayload{MessageID: second}, app.InterruptReply{})
			eventually(t, "original turn interrupted", func() bool {
				return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND status='interrupted'`, first) == 1
			})
			if e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status=?`, second, expected) != 1 {
				t.Fatal("steer control did not settle the same turn")
			}
			if e.count(`SELECT COUNT(*) FROM logical_turns WHERE message_id=? AND status='stopped'`, first) != 1 {
				t.Fatal("supplement stop left the original logical responsibility active")
			}
			if e.count(`SELECT COUNT(*) FROM logical_turn_executions WHERE logical_turn_id=(SELECT logical_turn_id FROM logical_turns WHERE message_id=?) AND repair=1`, first) != 0 {
				t.Fatal("owner stop queued automatic repair")
			}
			if len(businessStarts(h)) != 1 {
				t.Fatal("stop started another turn")
			}
		})
	}
}
