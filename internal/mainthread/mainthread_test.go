package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
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

func newEnv(t *testing.T, h *testkit.Harness) *env {
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
	e.m = New(a, contextbuilder.New(a, s, ids, contextbuilder.Config{}), tools.New(a, s, quiet()), Config{
		Instance: "main", Thread: harness.ThreadConfig{Cwd: "/agent", DeveloperInstructions: Instructions},
		Open: h.Open, RestartDelay: 20 * time.Millisecond, Log: quiet(),
	})
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
	d := dispatch.New(e.app, map[string]dispatch.Handler{app.MainThreadDestination: e.m}, e.signal, time.Now, dispatch.Config{PollInterval: 50 * time.Millisecond}, quiet())
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
