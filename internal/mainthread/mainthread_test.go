package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
	"github.com/GhostFlying/tend/internal/views"
)

var owner = protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "me", Client: "phone"}

type env struct {
	t       *testing.T
	store   *store.Store
	app     *app.App
	ids     protocol.IDGen
	h       *testkit.Harness
	m       *Manager
	signal  *dispatch.Signal
	cancel  context.CancelFunc
	stopped sync.WaitGroup
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newEnv(t *testing.T, h *testkit.Harness, opts ...func(*Config)) *env {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ids := protocol.NewULIDGen(time.Now, nil)
	sig := dispatch.NewSignal()
	a := app.New(s, ids, time.Now, app.WithNotifier(sig))
	e := &env{t: t, store: s, app: a, ids: ids, h: h, signal: sig}
	e.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
	}, app.SyncConfig{})
	cb := contextbuilder.New(a, s, ids, contextbuilder.Config{})
	cfg := Config{
		Instance: "main", Thread: harness.ThreadConfig{Cwd: "/agent", DeveloperInstructions: Instructions},
		Open: h.Open, RestartDelay: 20 * time.Millisecond, Log: quiet(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	e.m = New(a, cb, tools.New(a, s, cb, quiet()), cfg)
	e.start()
	t.Cleanup(func() {
		e.stop()
		s.Close()
	})
	return e
}

// start runs the manager and the dispatcher, as a tend process would.
func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	d := dispatch.New(e.app, map[string]dispatch.Handler{app.MainThreadDestination: e.m, app.MainThreadControlDestination: e.m.Control()}, e.signal, time.Now, dispatch.Config{PollInterval: 50 * time.Millisecond}, quiet())
	e.stopped.Go(func() { _ = e.m.Run(ctx) })
	e.stopped.Go(func() { _ = d.Run(ctx) })
}

func (e *env) stop() {
	if e.cancel != nil {
		e.cancel()
		e.stopped.Wait()
		e.cancel = nil
	}
}

func (e *env) exec(p protocol.Principal, op string, target protocol.Target, payload any, cmd app.Command) json.RawMessage {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	envl := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: b}
	res, err := e.app.Execute(context.Background(), p, envl, cmd)
	if err != nil || res.Status == protocol.OpFailed {
		e.t.Fatalf("%s: %+v, %v", op, res, err)
	}
	return res.Result
}

func (e *env) say(body string) string {
	e.t.Helper()
	var r struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, map[string]string{"body": body}, app.SubmitMessage{}), &r)
	return r.MessageID
}

func (e *env) count(query string, args ...any) int {
	var n int
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	return n
}

func (e *env) outboxStatus() string {
	var s string
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT status FROM outbox ORDER BY seq DESC LIMIT 1`).Scan(&s)
	})
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting until %s", what)
}

var spec = domain.Spec{Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
	Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}

func TestThreadConfigDelegatesExecution(t *testing.T) {
	cfg := ThreadConfig("/agent", "")
	if cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never" {
		t.Errorf("sandbox %q, approvals %q by %q", cfg.Sandbox, cfg.ApprovalPolicy, cfg.ApprovalsReviewer)
	}
	for _, feature := range []string{"features.shell_tool", "features.apply_patch_freeform", "features.code_mode", "features.js_repl", "features.multi_agent", "features.multi_agent_v2"} {
		if cfg.Config[feature] != false {
			t.Errorf("execution feature %s = %v", feature, cfg.Config[feature])
		}
	}
	if cfg.Config["web_search"] != "disabled" {
		t.Error("research must be delegated")
	}
	// The owner's configuration may add writable roots for their own
	// sessions; the hosted thread must not inherit them.
	if roots, ok := cfg.Config["sandbox_workspace_write.writable_roots"].([]string); !ok || len(roots) != 0 {
		t.Errorf("writable roots override = %v", cfg.Config["sandbox_workspace_write.writable_roots"])
	}
}

func TestInputNamesTheChosenOption(t *testing.T) {
	msg := store.MessageView{MessageID: "msg_2", Seq: 2, Body: "About the task: revise the spec",
		DecisionJSON: `{"decision_id":"dec_1","version":3,"option":"revise the spec"}`}
	want := `[message msg_2, choosing option "revise the spec" of decision dec_1 version 3]` + "\n" + msg.Body
	if got := input(msg); got != want {
		t.Errorf("input = %q, want %q", got, want)
	}
}

func TestMessageBecomesTask(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		res := th.CallTool(context.Background(), turn, "call_1", "tasks_create", map[string]any{"title": "fix the build", "spec": spec, "commit_seq": 1})
		if !res.Success {
			return "failed"
		}
		th.Say(turn, "item_1", "Created a task to fix the build.")
		return "completed"
	}
	e := newEnv(t, h)
	msg := e.say("Please fix the build.")

	eventually(t, "the turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	if n := e.count(`SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM conversation_bindings WHERE message_id = ? AND object_type = 'task'`, msg); n != 1 {
		t.Errorf("message bound to %d tasks, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent' AND reply_to = ?`, msg); n != 1 {
		t.Errorf("agent replies = %d, want 1", n)
	}
	starts := h.Starts()
	if len(starts) != 1 || starts[0].UserMessageID != msg || !strings.HasPrefix(starts[0].Text, "[message "+msg+"]") {
		t.Errorf("turn input = %+v", starts)
	}
	var thread string
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		thread, err = store.GetMainThread(context.Background(), tx.Q(), "main")
		return err
	})
	if inj := h.Injected(thread); len(inj) != 1 || !strings.Contains(inj[0], "kind=full") {
		t.Errorf("injected = %q, want one full snapshot", inj)
	}
	eventually(t, "the delivery is recorded", func() bool { return e.outboxStatus() == store.OutboxDelivered })
}

func TestToolUsesJoinTheConversation(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		th.UseTool(turn, "item_1", harness.ToolUse{Name: "tasks_list", Input: "{}", Status: protocol.ToolRunning})
		th.UseTool(turn, "item_1", harness.ToolUse{Name: "tasks_list", Input: "{}", Output: "no tasks", Status: protocol.ToolCompleted})
		th.UseTool(turn, "item_2", harness.ToolUse{Name: "shell", Input: "cat big", Output: strings.Repeat("x", 9000), Status: protocol.ToolFailed})
		// A start that arrives after the end does not take it back.
		th.UseTool(turn, "item_2", harness.ToolUse{Name: "shell", Input: "cat big", Status: protocol.ToolRunning})
		// A command moved to the background ends later.
		th.UseTool(turn, "item_4", harness.ToolUse{Name: "shell", Input: "make", Status: protocol.ToolRunning})
		th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turn, ItemID: "item_4", ItemType: "commandExecution",
			Tool: &harness.ToolUse{Name: "shell", Input: "make", Output: "building", Status: protocol.ToolRunning}})
		th.UseTool(turn, "item_4", harness.ToolUse{Name: "shell", Input: "make", Output: "built", Status: protocol.ToolCompleted})
		th.Say(turn, "item_3", "Nothing to do.")
		return "completed"
	}
	e := newEnv(t, h)
	msg := e.say("Anything open?")
	eventually(t, "the turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })

	var page protocol.ConversationPage
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.Conversation(context.Background(), tx.Q(), 0, 0, 10)
		return err
	})
	var got []string
	for _, it := range page.Items {
		s := it.Kind + ":" + it.Author
		if it.Tool != nil {
			s += ":" + it.Tool.Name + ":" + string(it.Tool.Status)
		}
		got = append(got, s)
		if it.Author == "main_agent" && it.ReplyTo != msg {
			t.Errorf("%s replies to %q, want the owner's message", s, it.ReplyTo)
		}
	}
	want := []string{"message:owner", "tool_use:main_agent:tasks_list:completed", "tool_use:main_agent:shell:failed",
		"tool_use:main_agent:shell:completed", "message:main_agent"}
	if !slices.Equal(got, want) || page.More {
		t.Errorf("conversation = %v (more %v), want %v", got, page.More, want)
	}
	if out := page.Items[2].Tool.Output; len(out) > 8<<10+64 || !strings.Contains(out, "[truncated: 8192 of 9000 bytes shown]") {
		t.Errorf("long output kept %d bytes: ...%q", len(out), out[max(0, len(out)-60):])
	}
	if out := page.Items[3].Tool.Output; out != "built" {
		t.Errorf("backgrounded command output = %q, want its final output", out)
	}
	if page.Items[0].State != protocol.MessageReplied {
		t.Errorf("owner message state = %q, want replied", page.Items[0].State)
	}
	// Recall and replies see messages only.
	if _, err := e.app.Message(context.Background(), page.Items[1].MessageID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a tool use reads as a message: %v", err)
	}
}

func TestTurnsAreSerial(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	running := 0
	overlap := false
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		mu.Lock()
		running++
		overlap = overlap || running > 1
		mu.Unlock()
		if strings.Contains(in.Text, "first") {
			<-gate
		}
		mu.Lock()
		running--
		mu.Unlock()
		return "completed"
	}
	e := newEnv(t, h)
	e.say("first")
	e.say("second")
	eventually(t, "the first turn starts", func() bool { return len(h.Starts()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := len(h.Starts()); n != 1 {
		t.Fatalf("turns started while the first one runs: %d", n)
	}
	close(gate)
	eventually(t, "both turns complete", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2 })
	if overlap {
		t.Error("turns overlapped")
	}
}

func TestFault_TurnStartUnknown(t *testing.T) {
	h := testkit.NewHarness()
	var mu sync.Mutex
	broken := true
	h.StartTurnErr = func(harness.TurnInput) error {
		mu.Lock()
		defer mu.Unlock()
		if broken {
			broken = false
			return errors.New("pipe broke mid-request")
		}
		return nil
	}
	e := newEnv(t, h)
	e.say("hello")
	eventually(t, "the delivery is marked unknown", func() bool { return e.outboxStatus() == store.OutboxUnknown })
	// The session that may still run the turn is replaced before the next
	// message gets a turn.
	eventually(t, "a new session takes over", func() bool { return h.Sessions() == 2 })
	e.say("are you there?")
	eventually(t, "the next message gets its turn", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := e.count(`SELECT COUNT(*) FROM outbox WHERE attempts > 1`); n != 0 {
		t.Error("an uncertain turn start was retried")
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE kind = 'outbox.unknown'`); n != 1 {
		t.Errorf("outbox.unknown events = %d, want 1", n)
	}
}

func TestRejectedTurnStartIsRetried(t *testing.T) {
	h := testkit.NewHarness()
	var mu sync.Mutex
	refusals := 1
	h.StartTurnErr = func(harness.TurnInput) error {
		mu.Lock()
		defer mu.Unlock()
		if refusals > 0 {
			refusals--
			return harness.ErrRejected
		}
		return nil
	}
	e := newEnv(t, h)
	e.say("hello")
	eventually(t, "the turn completes after the refusal", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
}

func TestCompactionTriggersFullSnapshot(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "compact") {
			th.Compact(turn)
		}
		return "completed"
	}
	e := newEnv(t, h)
	e.say("hello")
	eventually(t, "the first turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	e.say("compact please")
	eventually(t, "the second turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2 })
	e.say("and now?")
	eventually(t, "the third turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 3 })
	var thread string
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		thread, err = store.GetMainThread(context.Background(), tx.Q(), "main")
		return err
	})
	inj := h.Injected(thread)
	// The second turn had nothing new to inject; after its compaction the
	// third gets a full snapshot under a new epoch.
	if len(inj) != 2 || !strings.Contains(inj[1], "kind=full") || !strings.Contains(inj[1], "epoch=2") {
		t.Errorf("injections after compaction = %q", inj)
	}
}

func TestRestartResumesThread(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	e.say("one")
	eventually(t, "the first turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	h.Crash()
	eventually(t, "a new session resumes the thread", func() bool { return h.Sessions() == 2 && h.Resumed() == 1 })
	e.say("two")
	eventually(t, "the second turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2 })
	if n := e.count(`SELECT COUNT(DISTINCT thread_id) FROM main_turns`); n != 1 {
		t.Errorf("turns ran on %d threads, want the resumed one", n)
	}
}

func TestFault_TurnStartUnknownAfterRestart(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	e.stop()
	// A previous process recorded the turn as starting and died mid-send.
	id := e.say("hello")
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.PutMainTurn(context.Background(), store.MainTurn{RequestID: "x", MessageID: id, ThreadID: "th_old", Status: store.TurnStarting}, 0); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `UPDATE outbox SET status = 'sending', attempts = 1, request_id = 'x'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.start()
	eventually(t, "reconciliation marks the delivery unknown", func() bool { return e.outboxStatus() == store.OutboxUnknown })
	if n := len(h.Starts()); n != 0 {
		t.Errorf("an uncertain delivery was started again: %d", n)
	}
}

func TestFault_TurnRunningAtCrash(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	e.say("one")
	eventually(t, "the first turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	e.stop()
	// A previous process started the turn for "two" and died during it.
	id := e.say("two")
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		thread, err := store.GetMainThread(context.Background(), tx.Q(), "main")
		if err != nil {
			return err
		}
		if err := tx.PutMainTurn(context.Background(), store.MainTurn{RequestID: "x", MessageID: id, ThreadID: thread, TurnID: "turn_old", Status: store.TurnStarted}, 0); err != nil {
			return err
		}
		_, err = tx.Exec(context.Background(), `UPDATE outbox SET status = 'sending', attempts = 1, request_id = 'x' WHERE seq = (SELECT MAX(seq) FROM outbox)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.start()
	eventually(t, "the turn is settled and its delivery kept", func() bool {
		return e.outboxStatus() == store.OutboxDelivered && e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id = 'x' AND status = 'interrupted'`) == 1
	})
	if n := len(h.Starts()); n != 1 {
		t.Errorf("turn starts = %d, want the interrupted message not sent again", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE kind = 'conversation.message_state' AND object_id = ? AND body_json LIKE '%interrupted%'`, id); n != 1 {
		t.Errorf("interrupted state events = %d, want the owner told once", n)
	}
}

func TestFault_ToolRunningAtCrash(t *testing.T) {
	h := testkit.NewHarness()
	gate := make(chan struct{})
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		th.UseTool(turn, "item_1", harness.ToolUse{Name: "shell", Input: "sleep 100", Status: protocol.ToolRunning})
		<-gate
		return "completed"
	}
	e := newEnv(t, h)
	defer close(gate)
	e.say("wait")
	running := func() bool {
		return e.count(`SELECT COUNT(*) FROM conversation_tool_uses WHERE status = 'running'`) == 1
	}
	eventually(t, "the tool use is recorded", running)
	h.Crash()
	eventually(t, "the tool use is settled", func() bool {
		return e.count(`SELECT COUNT(*) FROM conversation_tool_uses WHERE status = 'interrupted'`) == 1
	})
	if n := e.count(`SELECT COUNT(*) FROM events WHERE kind = 'conversation.tool_use' AND body_json LIKE '%interrupted%'`); n != 1 {
		t.Errorf("interrupted tool use events = %d, want 1", n)
	}
}

func TestFault_ToolRunningInATurnNeverAcknowledged(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	// The tool use was recorded, but turn/start never answered, so tend
	// has no turn ID for it.
	agent := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "th_lost"}
	e.exec(agent, "conversation.record_tool_use", protocol.Target{Type: "message"},
		map[string]any{"turn_id": "turn_unknown", "item_id": "item_1", "tool": "shell", "input": "ls", "status": "running"}, app.RecordToolUse{})
	if err := e.app.SettleOpenTurns(context.Background(), "th_lost"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM conversation_tool_uses WHERE status = 'interrupted'`); n != 1 {
		t.Errorf("interrupted tool uses = %d, want 1", n)
	}
}

func TestInterruptReply(t *testing.T) {
	h := testkit.NewHarness()
	gate := make(chan struct{})
	var once sync.Once
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "long") {
			<-gate
			return "" // stopped by the interrupt
		}
		th.Say(turn, "item_"+turn, "done")
		return "completed"
	}
	e := newEnv(t, h)
	defer once.Do(func() { close(gate) })
	stop := func(msg, requestID string) json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(protocol.InterruptReplyPayload{MessageID: msg})
		res, err := e.app.Execute(context.Background(), owner, &protocol.Envelope{RequestID: requestID, Operation: "conversation.interrupt_reply",
			Target: protocol.Target{Type: "message", ID: msg}, Payload: b}, app.InterruptReply{})
		if err != nil || res.Status == protocol.OpFailed {
			t.Fatalf("interrupt_reply: %+v %v", res, err)
		}
		return res.Result
	}

	long := e.say("a long one")
	eventually(t, "the turn runs", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'started'`) == 1 })
	retried := e.ids.New(protocol.PrefixRequest)
	stop(long, retried)
	stop(long, retried) // a retry of the same request
	eventually(t, "the turn is interrupted", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id = ? AND status = 'interrupted'`, long) == 1
	})
	if n := e.count(`SELECT COUNT(*) FROM outbox WHERE destination = ?`, app.MainThreadControlDestination); n != 1 {
		t.Errorf("stops queued = %d, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE kind = 'conversation.message_state' AND object_id = ? AND body_json LIKE '%interrupted%'`, long); n != 1 {
		t.Errorf("interrupted state events = %d, want 1", n)
	}
	once.Do(func() { close(gate) })

	// The next message is handled as usual, and stopping its finished
	// reply changes nothing.
	next := e.say("a short one")
	eventually(t, "the next turn completes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id = ? AND status = 'completed'`, next) == 1
	})
	var res struct {
		State protocol.MessageState `json:"state"`
	}
	if err := json.Unmarshal(stop(next, e.ids.New(protocol.PrefixRequest)), &res); err != nil || res.State != protocol.MessageReplied {
		t.Errorf("stopping a finished reply = %+v %v, want its state replied", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM outbox WHERE destination = ?`, app.MainThreadControlDestination); n != 1 {
		t.Errorf("stops queued = %d, want still 1", n)
	}
}

func TestInterruptRefusedWhileRunningIsRetried(t *testing.T) {
	h := testkit.NewHarness()
	gate := make(chan struct{})
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		<-gate
		return ""
	}
	var mu sync.Mutex
	refusals := 0
	h.InterruptErr = func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		if refusals++; refusals == 1 {
			return fmt.Errorf("%w: overloaded", harness.ErrRejected)
		}
		return nil
	}
	e := newEnv(t, h)
	defer close(gate)
	msg := e.say("long")
	eventually(t, "the turn runs", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'started'`) == 1 })
	e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: msg}, protocol.InterruptReplyPayload{MessageID: msg}, app.InterruptReply{})
	eventually(t, "the turn is interrupted after a refusal", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id = ? AND status = 'interrupted'`, msg) == 1
	})
}

func TestFault_InterruptUnanswered(t *testing.T) {
	h := testkit.NewHarness()
	gate := make(chan struct{})
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		<-gate
		return "completed"
	}
	h.InterruptErr = func(ctx context.Context, _ string) error {
		<-ctx.Done() // the answer never comes
		return ctx.Err()
	}
	e := newEnv(t, h, func(c *Config) { c.InterruptTimeout = 50 * time.Millisecond })
	defer close(gate)
	msg := e.say("long")
	eventually(t, "the turn runs", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'started'`) == 1 })
	e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: msg}, protocol.InterruptReplyPayload{MessageID: msg}, app.InterruptReply{})
	eventually(t, "the stop is marked unknown, not resent", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE destination = ? AND status = 'unknown'`, app.MainThreadControlDestination) == 1
	})
}

func TestFault_InterruptReplyLost(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	msg := e.say("one")
	eventually(t, "the turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	payload, _ := json.Marshal(app.InterruptTurn{MessageID: msg})
	d := app.Delivery{Destination: app.MainThreadControlDestination, Operation: app.OpInterruptTurn, Payload: payload}
	if r := e.m.Control().Reconcile(context.Background(), d); r.Outcome != app.Delivered {
		t.Errorf("reconcile after the turn ended = %+v, want delivered", r)
	}
	// A stop cut short while its turn still looks open is not sent again.
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		return tx.PutMainTurn(context.Background(), store.MainTurn{RequestID: "x", MessageID: msg, ThreadID: "th", TurnID: "t", Status: store.TurnStarted}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if r := e.m.Control().Reconcile(context.Background(), d); r.Outcome != app.Unknown {
		t.Errorf("reconcile of an open turn = %+v, want unknown", r)
	}
}

func TestReplayedToolCallKeepsItsMessage(t *testing.T) {
	h := testkit.NewHarness()
	var first string
	var replay harness.ToolResult
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		args := map[string]any{"title": "fix", "spec": spec, "commit_seq": 1}
		if first == "" {
			first = turn
			th.CallTool(context.Background(), turn, "call_1", "tasks_create", args)
		} else {
			// The harness repeats the first turn's call during this one.
			replay = th.CallTool(context.Background(), first, "call_1", "tasks_create", args)
		}
		return "completed"
	}
	e := newEnv(t, h)
	e.say("create it")
	eventually(t, "the first turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	e.say("something else")
	eventually(t, "the second turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2 })
	if !replay.Success {
		t.Errorf("replayed call = %s, want the original result", replay.Text)
	}
	if n := e.count(`SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

func TestEarlyReplyKeepsItsMessage(t *testing.T) {
	h := testkit.NewHarness()
	h.BeforeAnswer = func(th *testkit.FakeThread, turn string) { th.Say(turn, "item_1", "quick answer") }
	e := newEnv(t, h)
	msg := e.say("hi")
	eventually(t, "the turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 1 })
	if n := e.count(`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent' AND reply_to = ?`, msg); n != 1 {
		t.Errorf("early reply not tied to its message")
	}
}

func TestLaggingClientIsVisible(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	e.say("one")
	e.say("two")
	eventually(t, "two turns complete", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2 })
	// A client that last showed the first message answers.
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, map[string]any{"body": "yes", "last_seen_seq": 1}, app.SubmitMessage{})
	eventually(t, "the third turn starts", func() bool { return len(h.Starts()) >= 3 })
	if in := h.Starts()[2].Text; !strings.Contains(in, "having seen messages up to #1") {
		t.Errorf("turn input = %q", in)
	}
}
