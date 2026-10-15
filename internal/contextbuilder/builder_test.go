package contextbuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

const thread = "harness-1/th_1"

var owner = protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "me", Client: "c"}

type env struct {
	t     *testing.T
	app   *app.App
	store *store.Store
	ids   protocol.IDGen
	b     *Builder
	msgs  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := testkit.NewClock()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 11)
	a := app.New(s, ids, clock.Now)
	e := &env{t: t, app: a, store: s, ids: ids, b: New(a, s, ids, Config{})}
	e.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, nil, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
	}, app.SyncConfig{})
	return e
}

func (e *env) exec(p protocol.Principal, op string, target protocol.Target, rev *int64, payload any, cmd app.Command) json.RawMessage {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	envl := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, ExpectedControlRevision: rev, Payload: b}
	res, err := e.app.Execute(context.Background(), p, envl, cmd)
	if err != nil || res.Status != protocol.OpSucceeded {
		e.t.Fatalf("%s: %+v, %v", op, res, err)
	}
	return res.Result
}

func (e *env) task(title string, constraints ...string) (string, int64) {
	e.t.Helper()
	res := e.exec(owner, "tasks.create", protocol.Target{Type: "task"}, nil, map[string]any{"title": title, "spec": domain.Spec{
		Goal: title, Acceptance: []string{"works"}, Delivery: domain.DeliveryCommit, Constraints: constraints,
		Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}}, app.CreateTask{})
	var r struct {
		TaskID          string `json:"task_id"`
		ControlRevision int64  `json:"control_revision"`
	}
	_ = json.Unmarshal(res, &r)
	return r.TaskID, r.ControlRevision
}

func (e *env) reprioritize(id string, rev, priority int64) {
	e.t.Helper()
	e.exec(owner, "tasks.update_intent", protocol.Target{Type: "task", ID: id}, &rev, map[string]any{"priority": priority}, app.UpdateIntent{})
}

// message stores a conversation message, as the main thread manager will.
func (e *env) message(author, body, replyTo string, bindings ...string) (string, int64) {
	e.t.Helper()
	e.msgs++
	id := fmt.Sprintf("msg_%d", e.msgs)
	var seq int64
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		res, err := tx.Exec(context.Background(), `INSERT INTO conversation_messages (message_id, author, body, reply_to, created_at) VALUES (?, ?, ?, NULLIF(?, ''), 0)`,
			id, author, body, replyTo)
		if err != nil {
			return err
		}
		seq, _ = res.LastInsertId()
		for _, b := range bindings {
			typ, oid, _ := strings.Cut(b, ":")
			if _, err := tx.Exec(context.Background(), `INSERT INTO conversation_bindings (message_id, object_type, object_id, kind) VALUES (?, ?, ?, 'explicit')`, id, typ, oid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return id, seq
}

func (e *env) prepare(seq int64) *Packet {
	e.t.Helper()
	p, err := e.b.Prepare(context.Background(), thread, seq)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) baseline() store.Baseline {
	var b store.Baseline
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		b, err = store.GetBaseline(context.Background(), tx.Q(), thread)
		return err
	})
	return b
}

func TestFullThenNothingThenDelta(t *testing.T) {
	e := newEnv(t)
	id, rev := e.task("fix windows", "keep the public API")
	full := e.prepare(0)
	if full == nil || !full.Full || !strings.Contains(full.Text, "kind=full") || !strings.Contains(full.Text, "constraints: keep the public API") {
		t.Fatalf("full packet = %+v", full)
	}
	if err := e.b.Confirm(context.Background(), full.ID); err != nil {
		t.Fatal(err)
	}
	if p := e.prepare(0); p != nil {
		t.Fatalf("packet with nothing new = %q", p.Text)
	}
	e.reprioritize(id, rev, 7)
	delta := e.prepare(0)
	if delta == nil || delta.Full || !strings.Contains(delta.Text, "task.priority_changed") || !strings.Contains(delta.Text, "now: "+id) {
		t.Fatalf("delta = %+v", delta)
	}
	if !strings.Contains(delta.Text, "priority 7") {
		t.Errorf("delta lacks the task's current state:\n%s", delta.Text)
	}
}

func TestFault_InjectFailureInvalidatesBaseline(t *testing.T) {
	e := newEnv(t)
	id, rev := e.task("fix windows")
	full := e.prepare(0)
	_ = e.b.Confirm(context.Background(), full.ID)
	before := e.baseline()

	e.reprioritize(id, rev, 3)
	delta := e.prepare(0)
	if err := e.b.Fail(context.Background(), delta.ID, true); err != nil {
		t.Fatal(err)
	}
	if after := e.baseline(); after.Watermark != before.Watermark || after.Valid {
		t.Errorf("baseline after a failed injection = %+v, want watermark %d and invalid", after, before.Watermark)
	}
	next := e.prepare(0)
	if next == nil || !next.Full || !strings.Contains(next.Text, "epoch=2") || next.ID == delta.ID {
		t.Errorf("after a failed injection: %+v, want a new full snapshot", next)
	}
}

func TestFault_CompactionMidTurn(t *testing.T) {
	e := newEnv(t)
	e.task("fix windows", "do not touch the installer")
	e.exec(owner, "decisions.open", protocol.Target{Type: "decision"}, nil, map[string]any{"question": "Ship on Friday?", "options": []string{"yes", "no"}}, app.OpenDecision{})
	full := e.prepare(0)
	_ = e.b.Confirm(context.Background(), full.ID)
	// The harness compacted the thread: injected messages are gone.
	if err := e.b.Invalidate(context.Background(), thread); err != nil {
		t.Fatal(err)
	}
	p := e.prepare(0)
	if p == nil || !p.Full || !strings.Contains(p.Text, "epoch=2") {
		t.Fatalf("after compaction: %+v", p)
	}
	for _, want := range []string{"Ship on Friday?", "do not touch the installer"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("snapshot after compaction lacks %q:\n%s", want, p.Text)
		}
	}
}

func TestUnconfirmedPacketAfterCrash(t *testing.T) {
	e := newEnv(t)
	id, rev := e.task("fix windows")
	_ = e.b.Confirm(context.Background(), e.prepare(0).ID)
	e.reprioritize(id, rev, 2)
	lost := e.prepare(0) // tend stops before injecting or confirming

	restarted := New(e.app, e.store, e.ids, Config{})
	p, err := restarted.Prepare(context.Background(), thread, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || !p.Full {
		t.Fatalf("after a crash mid-injection: %+v, want a full snapshot", p)
	}
	var status string
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		pk, err := store.GetPacket(context.Background(), tx.Q(), lost.ID)
		status = pk.Status
		return err
	})
	if status != store.PacketUnknown {
		t.Errorf("unconfirmed packet = %s, want unknown", status)
	}
}

func TestFault_BackgroundEventKeepsFocus(t *testing.T) {
	e := newEnv(t)
	win, _ := e.task("fix windows")
	wpt, wptRev := e.task("run WPT")
	e.message("owner", "For the Windows fix, should we patch the linker flags or switch toolchains?", "", "task:"+win)
	e.message("main_agent", "Two options: 1) patch the linker flags, 2) switch to the clang toolchain.", "msg_1", "task:"+win)
	_ = e.b.Confirm(context.Background(), e.prepare(3).ID)

	// A background change arrives before the owner answers.
	e.reprioritize(wpt, wptRev, 5)
	_, seq := e.message("owner", "Go with the second one.", "msg_2")

	delta := e.prepare(seq)
	if delta == nil || strings.Contains(delta.Text, "Recent conversation") || !strings.Contains(delta.Text, "do not change what the owner is talking about") {
		t.Fatalf("delta = %+v", delta)
	}
	// After a compaction the full snapshot recalls the discussion, and the
	// background task stays out of it.
	_ = e.b.Confirm(context.Background(), delta.ID)
	_ = e.b.Invalidate(context.Background(), thread)
	full := e.prepare(seq)
	focus := sectionOf(full.Text, "Recent conversation")
	if !strings.Contains(focus, "switch to the clang toolchain") || !strings.Contains(focus, "task:"+win) {
		t.Errorf("focus lacks the discussion:\n%s", focus)
	}
	if strings.Contains(focus, "WPT") || strings.Contains(focus, "Go with the second one") {
		t.Errorf("focus holds the background task or the message being handled:\n%s", focus)
	}
}

func sectionOf(text, title string) string {
	_, rest, ok := strings.Cut(text, "## "+title+"\n")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

func TestRenderBudget(t *testing.T) {
	s := Snapshot{PacketID: "pkt_1", Epoch: 1, Full: true, Decisions: []string{"dec_1 v1: Ship it? Options: yes / no"}}
	for i := range 200 {
		s.Tasks = append(s.Tasks, fmt.Sprintf("task_%03d %q: queued; constraints: keep %d stable", i, "task", i))
	}
	out := Render(s, 3000)
	if len(out) > 3200 {
		t.Errorf("rendered %d characters for a 3000 budget", len(out))
	}
	if !strings.Contains(out, "Ship it?") || !strings.Contains(out, "more open tasks; see tasks_list") {
		t.Errorf("budgeted packet:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- task_") && !strings.HasSuffix(line, "stable") {
			t.Errorf("a task line was cut: %q", line)
		}
	}
}

func TestInvalidationWinsOverALateConfirm(t *testing.T) {
	e := newEnv(t)
	e.task("fix windows")
	p := e.prepare(0)
	// Compaction is seen while the turn after the injection is starting.
	_ = e.b.Invalidate(context.Background(), thread)
	if err := e.b.Confirm(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if next := e.prepare(0); next == nil || !next.Full {
		t.Errorf("after invalidation and a late confirm: %+v, want a full snapshot", next)
	}
}

func TestDeltaCarriesDecisionsAndRunTasks(t *testing.T) {
	e := newEnv(t)
	id, _ := e.task("fix windows")
	_ = e.b.Confirm(context.Background(), e.prepare(0).ID)
	res := e.exec(owner, "decisions.open", protocol.Target{Type: "decision"}, nil, map[string]any{"question": "Ship on Friday?", "options": []string{"yes", "no"}}, app.OpenDecision{})
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	_ = json.Unmarshal(res, &opened)
	e.exec(owner, "decisions.respond", protocol.Target{Type: "decision", ID: opened.DecisionID}, nil, map[string]any{"version": 1, "option": "no"}, app.AnswerDecision{})
	// A run of the task goes unknown.
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO runs (run_id, task_id, action_id, generation, role, desired_state, observed_state, created_at, updated_at)
			SELECT 'run_x', task_id, action_id, 1, 'implement', 'run', 'unknown', 0, 0 FROM plan_actions WHERE task_id = ?`, id)
		if err != nil {
			return err
		}
		_, err = tx.InsertEvent(context.Background(), store.EventRow{EventID: "evt_x", Source: "app", ObjectType: "run", ObjectID: "run_x", Kind: "run.unknown", BodyJSON: `{}`})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delta := e.prepare(0)
	decisions := sectionOf(delta.Text, "Decisions")
	if !strings.Contains(decisions, "opened: "+opened.DecisionID) || !strings.Contains(decisions, `"option":"no"`) {
		t.Errorf("decisions in the delta:\n%s", decisions)
	}
	changes := sectionOf(delta.Text, "Changes")
	if !strings.Contains(changes, "run_x of "+id+" run.unknown") || !strings.Contains(changes, "now: "+id) {
		t.Errorf("run change not tied to its task:\n%s", changes)
	}
}
