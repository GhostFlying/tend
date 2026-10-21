package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

// liveOrca is an orca that keeps its terminals and agents.
type liveOrca struct {
	mu     sync.Mutex
	wt     string
	terms  []Terminal
	agents map[string]*Agent // by pane key
	ops    map[string]string // launch operation → handle
	next   int
	// loseLaunch loses the answer of this many launches; launchKind
	// overrides the outcome.
	loseLaunch int
	launchKind string
	// loseSend loses the answer of this many sends; onLaunch runs as a
	// launch is taken; lateEsc delays reporting an interruption.
	loseSend int
	failPs   bool
	onLaunch func() error
	lateEsc  bool
	calls    [][]string
}

func newLiveOrca() *liveOrca {
	return &liveOrca{wt: "repo-1::/w", agents: map[string]*Agent{}, ops: map[string]string{}}
}

func ok(result any) []byte {
	b, _ := json.Marshal(map[string]any{"id": "r", "ok": true, "result": result})
	return b
}

func flag(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func now() int64 { return time.Now().UnixMilli() }

func (f *liveOrca) exec(_ context.Context, dir string, args []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slices.Clone(args))
	switch args[0] + " " + args[1] {
	case "worktree show":
		return ok(map[string]any{"worktree": map[string]any{"id": f.wt, "path": strings.TrimPrefix(flag(args, "--worktree"), "path:")}}), nil
	case "agent launch":
		handle := f.ops[flag(args, "--retry-launch")]
		if handle == "" {
			f.next++
			handle = fmt.Sprintf("term_%d", f.next)
			t := Terminal{Handle: handle, WorktreeID: f.wt, Connected: true, TabID: "tab", LeafID: fmt.Sprint(f.next)}
			f.terms = append(f.terms, t)
			f.agents[t.PaneKey()] = &Agent{PaneKey: t.PaneKey(), State: stateWorking, Prompt: flag(args, "--prompt"), StateStartedAt: now()}
			op := fmt.Sprintf("op-%d", f.next)
			f.ops[op] = handle
			if f.loseLaunch > 0 {
				f.loseLaunch--
				b, _ := json.Marshal(map[string]any{"id": "r", "ok": false, "error": map[string]any{
					"code": "agent_session_operation_unknown", "message": "lost", "data": map[string]any{"operationId": op}}})
				return b, nil
			}
		}
		if f.onLaunch != nil {
			if err := f.onLaunch(); err != nil {
				return nil, err
			}
		}
		kind := cmpOr(f.launchKind, OutcomeTerminal)
		t := f.term(handle)
		return ok(map[string]any{"outcome": map[string]any{"kind": kind, "handle": handle, "paneKey": t.PaneKey()}, "worktreeId": f.wt}), nil
	case "terminal list":
		return ok(map[string]any{"terminals": f.terms}), nil
	case "worktree ps":
		if f.failPs {
			return nil, errors.New("orca is restarting")
		}
		var agents []Agent
		for _, a := range f.agents {
			agents = append(agents, *a)
		}
		return ok(map[string]any{"worktrees": []any{map[string]any{"worktreeId": f.wt, "agents": agents}}}), nil
	case "terminal send":
		a := f.agents[f.term(flag(args, "--terminal")).PaneKey()]
		text := flag(args, "--text")
		switch {
		case text == esc:
			if a.State == stateWorking && !f.lateEsc {
				a.State, a.Interrupted, a.StateStartedAt = stateDone, true, now()
			}
		case slices.Contains(args, "--enter"):
			if flag(args, "--retry-request") == "" || a.Prompt != text {
				a.Prompt, a.State, a.Interrupted, a.StateStartedAt = text, stateWorking, false, now()
			}
			if f.loseSend > 0 {
				f.loseSend--
				b, _ := json.Marshal(map[string]any{"id": "r", "ok": false, "error": map[string]any{
					"code": "runtime_timeout", "message": "lost", "data": map[string]any{"orchestrationRequestId": "req-1"}}})
				return b, nil
			}
		}
		return ok(map[string]any{"send": map[string]any{"handle": flag(args, "--terminal"), "accepted": true}}), nil
	case "terminal wait":
		a := f.agents[f.term(flag(args, "--terminal")).PaneKey()]
		if a == nil {
			return nil, errors.New("no such terminal")
		}
		if a.State == stateWorking {
			// orca fails a wait whose condition did not hold in time.
			b, _ := json.Marshal(map[string]any{"id": "r", "ok": false, "error": map[string]any{"code": "timeout", "message": "timed out"}})
			return b, nil
		}
		return ok(map[string]any{"wait": map[string]any{"satisfied": true, "status": a.State}}), nil
	case "terminal close":
		h := flag(args, "--terminal")
		delete(f.agents, f.term(h).PaneKey())
		f.terms = slices.DeleteFunc(f.terms, func(t Terminal) bool { return t.Handle == h })
		return ok(map[string]any{}), nil
	}
	return nil, fmt.Errorf("unexpected orca command %q", args)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (f *liveOrca) term(handle string) Terminal {
	for _, t := range f.terms {
		if t.Handle == handle {
			return t
		}
	}
	return Terminal{}
}

func (f *liveOrca) prompt(pane string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agents[pane].Prompt
}

// finish ends the agent's turn in handle with reply.
func (f *liveOrca) finish(handle, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[f.term(handle).PaneKey()]
	a.State, a.LastAssistantMessage, a.StateStartedAt, a.Interrupted = stateDone, reply, now()+1, false
}

// interrupt reports an interruption orca saw late.
func (f *liveOrca) interrupt(handle string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[f.term(handle).PaneKey()]
	a.State, a.Interrupted, a.StateStartedAt = stateDone, true, now()
}

func (f *liveOrca) count(cmd string, pred func([]string) bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c[0]+" "+c[1] == cmd && (pred == nil || pred(c)) {
			n++
		}
	}
	return n
}

type workerFixture struct {
	t      *testing.T
	orca   *liveOrca
	ws     *Workers
	dir    string
	thread string
	saves  []workerState
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	sockets, err := os.MkdirTemp("", "tw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	f := &workerFixture{t: t, orca: newLiveOrca(), dir: dir}
	f.ws = NewWorkers(WorkerOptions{
		Client: &Client{Exec: f.orca.exec}, Agent: protocol.AgentCodex, BridgeCommand: "/opt/tend-runner", BridgeArgs: []string{"worker-mcp"},
		SocketDir: sockets, IDs: testkit.NewIDGen(testkit.NewClock(), 1), Now: time.Now,
		Settle: 50 * time.Millisecond, Poll: time.Millisecond, WaitChunk: time.Millisecond,
	})
	return f
}

func (f *workerFixture) spec(role protocol.RunRole) harness.WorkerSpec {
	return harness.WorkerSpec{
		RunID: "run_1", Role: role, Dir: f.dir, Guide: "On branch `me/x`.", ScratchDir: "/scratch",
		Spec: json.RawMessage(`{"goal":"add a flag","acceptance":["it builds"]}`), Thread: f.thread,
		OnThread: func(s string) error {
			f.thread = s
			var st workerState
			_ = json.Unmarshal([]byte(s), &st)
			f.saves = append(f.saves, st)
			return nil
		},
	}
}

// start starts a worker and collects its events until it ends.
func (f *workerFixture) start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, *[]harness.WorkerEvent, chan struct{}) {
	f.t.Helper()
	w, err := f.ws.Start(ctx, spec)
	if err != nil {
		f.t.Fatalf("start: %v", err)
	}
	var events []harness.WorkerEvent
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range w.Events() {
			events = append(events, ev)
		}
	}()
	return w, &events, drained
}

// submit calls submit_verdict as the agent would, through its MCP server.
func (f *workerFixture) submit(verdict protocol.Verdict) {
	f.t.Helper()
	state, err := bridge.StatePath(context.Background(), f.dir)
	if err != nil {
		f.t.Fatal(err)
	}
	var out bytes.Buffer
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"verdict":"` + string(verdict) + `","evidence":{"ran":"go build"}}}}` + "\n"
	if err := bridge.ServeMCP(context.Background(), state, "run_1", strings.NewReader(in), &out); err != nil || strings.Contains(out.String(), "isError") {
		f.t.Fatalf("submit_verdict = %s, %v", out.String(), err)
	}
}

func (f *workerFixture) state() workerState {
	var st workerState
	_ = json.Unmarshal([]byte(f.thread), &st)
	return st
}

func kinds(evs []harness.WorkerEvent) []string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func TestOrcaWorkerImplements(t *testing.T) {
	f := newWorkerFixture(t)
	w, events, drained := f.start(context.Background(), f.spec(protocol.RoleImplement))
	st := f.state()
	if len(f.saves) < 2 || f.saves[0].Marker == "" || f.saves[0].Terminal != "" || st.Terminal != "term_1" || st.Pane != "tab:1" {
		t.Fatalf("journaled %+v, want the marker before the launch and the terminal after", f.saves)
	}
	prompt := f.orca.prompt("tab:1")
	if !strings.Contains(prompt, st.Marker) || !strings.Contains(prompt, "add a flag") || !strings.Contains(prompt, "On branch `me/x`.") ||
		strings.Contains(prompt, "AGENTS.md describes the workspace") {
		t.Errorf("prompt = %q", prompt)
	}
	cfg := filepath.Join(f.dir, ".codex", "config.toml")
	if _, err := os.Stat(cfg); err != nil {
		t.Errorf("bridge config while working: %v", err)
	}
	f.orca.finish("term_1", "added the flag")
	res, err := w.Wait()
	<-drained
	if err != nil || res.Summary != "added the flag" || res.Verdict != nil {
		t.Fatalf("result = %+v, %v", res, err)
	}
	got := kinds(*events)
	if len(got) < 3 || got[0] != "turn.started" || got[len(got)-1] != "turn.completed" || !slices.Contains(got, "agent.state") {
		t.Errorf("events = %q", got)
	}
	if last := (*events)[len(*events)-1]; !strings.Contains(string(last.Body), "added the flag") {
		t.Errorf("last event = %s, want the reply", last.Body)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("bridge config left behind: %v", err)
	}
	if f.orca.count("terminal close", nil) != 0 {
		t.Error("a finished worker's terminal was closed")
	}
}

func TestOrcaWorkerTakesTheVerdictThroughTheBridge(t *testing.T) {
	f := newWorkerFixture(t)
	w, _, drained := f.start(context.Background(), f.spec(protocol.RoleAccept))
	f.submit(protocol.VerdictPassed)
	f.orca.finish("term_1", "looks good")
	res, err := w.Wait()
	<-drained
	if err != nil || res.Verdict == nil || res.Verdict.Verdict != protocol.VerdictPassed {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

func TestOrcaWorkerPauseKeepsTheAgent(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrPaused))
	if _, err := w.Wait(); !errors.Is(err, harness.ErrPaused) {
		t.Fatalf("wait = %v, want the pause", err)
	}
	<-drained
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--text") == esc }); n != 1 || f.orca.count("terminal close", nil) != 0 {
		t.Fatalf("pause sent %d escapes, closed %d terminals; want the turn interrupted and the terminal kept", n, f.orca.count("terminal close", nil))
	}
	// Resume: the same agent gets a turn to continue.
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	if n := f.orca.count("agent launch", nil); n != 1 {
		t.Fatalf("%d launches, want the agent kept", n)
	}
	if p := f.orca.prompt("tab:1"); !strings.Contains(p, "interrupted") {
		t.Errorf("continuation = %q", p)
	}
	f.orca.finish("term_1", "done after all")
	if res, err := w.Wait(); err != nil || res.Summary != "done after all" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	<-drained
}

func TestOrcaWorkerStopClosesItsTerminal(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrStopped))
	if _, err := w.Wait(); !errors.Is(err, harness.ErrStopped) {
		t.Fatalf("wait = %v", err)
	}
	<-drained
	if n := f.orca.count("terminal close", func(c []string) bool { return flag(c, "--terminal") == "term_1" }); n != 1 {
		t.Errorf("closed %d times, want the worker's terminal closed", n)
	}
}

func TestOrcaWorkerWaitsForTheTurnUnderWay(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	// The runner restarts while the agent works: the restart interrupts
	// nothing it cannot continue, and the next attempt sends nothing new
	// while the agent still works.
	cancel()
	w.Wait()
	<-drained
	f.orca.mu.Lock()
	f.orca.agents["tab:1"].State, f.orca.agents["tab:1"].Interrupted = stateWorking, false
	f.orca.mu.Unlock()
	sends := f.orca.count("terminal send", nil)
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	f.orca.finish("term_1", "finished")
	if res, err := w.Wait(); err != nil || res.Summary != "finished" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	<-drained
	if n := f.orca.count("terminal send", nil) - sends; n != 0 || f.orca.count("agent launch", nil) != 1 {
		t.Errorf("%d sends and %d launches after the restart, want none", n, f.orca.count("agent launch", nil)-1)
	}
}

func TestOrcaWorkerFinishedWhileAway(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel()
	w.Wait()
	<-drained
	f.orca.finish("term_1", "done meanwhile")
	f.orca.mu.Lock()
	f.orca.agents["tab:1"].Interrupted = false
	f.orca.mu.Unlock()
	sends := f.orca.count("terminal send", nil)
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	if res, err := w.Wait(); err != nil || res.Summary != "done meanwhile" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	<-drained
	if n := f.orca.count("terminal send", nil) - sends; n != 0 {
		t.Errorf("%d sends, want the finished turn taken as it is", n)
	}
}

func TestFault_OrcaLaunchReplyLost(t *testing.T) {
	f := newWorkerFixture(t)
	f.orca.loseLaunch = 1
	w, _, drained := f.start(context.Background(), f.spec(protocol.RoleImplement))
	if n := f.orca.count("agent launch", func(c []string) bool { return flag(c, "--retry-launch") == "op-1" }); n != 1 {
		t.Fatalf("%d replays, want the lost launch replayed", n)
	}
	f.orca.mu.Lock()
	terms := len(f.orca.terms)
	f.orca.mu.Unlock()
	if terms != 1 || f.state().Terminal != "term_1" || f.state().Launch != "" {
		t.Fatalf("%d terminals, state %+v", terms, f.state())
	}
	if !slices.ContainsFunc(f.saves, func(s workerState) bool { return s.Launch == "op-1" }) {
		t.Errorf("journaled %+v, want the operation recorded before the replay", f.saves)
	}
	f.orca.finish("term_1", "ok")
	w.Wait()
	<-drained
}

func TestFault_RunnerStoppedMidLaunch(t *testing.T) {
	f := newWorkerFixture(t)
	// The launch went through, but the runner stopped before its answer.
	w, _, drained := f.start(context.Background(), f.spec(protocol.RoleImplement))
	f.orca.finish("term_1", "x")
	w.Wait()
	<-drained
	st := f.state()
	f.orca.mu.Lock()
	f.orca.agents["tab:1"].State, f.orca.agents["tab:1"].StateStartedAt = stateWorking, now()
	f.orca.mu.Unlock()
	b, _ := json.Marshal(workerState{Marker: st.Marker, Asked: now(), Turn: now()})
	f.thread = string(b)
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	if n := f.orca.count("agent launch", nil); n != 1 || f.state().Terminal != "term_1" {
		t.Fatalf("%d launches, state %+v; want the agent found by its marker", n, f.state())
	}
	f.orca.finish("term_1", "y")
	w.Wait()
	<-drained

	// Nothing shows up within the settle time: launch again.
	b, _ = json.Marshal(workerState{Marker: "lch_lost", Asked: now() - time.Hour.Milliseconds()})
	f.thread = string(b)
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	if n := f.orca.count("agent launch", nil); n != 2 || f.state().Terminal != "term_2" || f.state().Marker == "lch_lost" {
		t.Fatalf("%d launches, state %+v; want a new launch with a new marker", n, f.state())
	}
	f.orca.finish("term_2", "z")
	w.Wait()
	<-drained
}

func TestOrcaWorkerRefusesAChat(t *testing.T) {
	f := newWorkerFixture(t)
	f.orca.launchKind = "structured"
	_, err := f.ws.Start(context.Background(), f.spec(protocol.RoleImplement))
	if err == nil || !strings.Contains(err.Error(), "turn off the Chat UI") {
		t.Fatalf("start = %v, want the chat refused", err)
	}
	if f.orca.count("terminal close", nil) != 1 || f.state().Marker != "" {
		t.Errorf("chat not closed, or state %+v kept", f.state())
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("bridge config left behind: %v", err)
	}
}

func TestOrcaWorkerRelaunchesAGoneAgent(t *testing.T) {
	f := newWorkerFixture(t)
	b, _ := json.Marshal(workerState{Marker: "lch_old", Terminal: "term_9", Pane: "tab:9", Turn: now()})
	f.thread = string(b)
	spec := f.spec(protocol.RoleImplement)
	spec.Attempt = 2
	w, _, drained := f.start(context.Background(), spec)
	st := f.state()
	if st.Terminal != "term_1" || st.Marker == "lch_old" || !strings.Contains(f.orca.prompt("tab:1"), "earlier attempt") {
		t.Fatalf("state %+v, prompt %q; want a new agent with the brief", st, f.orca.prompt("tab:1"))
	}
	f.orca.finish("term_1", "ok")
	w.Wait()
	<-drained
}

func TestOrcaWorkerAsksAcceptanceAgainForItsVerdict(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleAccept))
	cancel()
	w.Wait()
	<-drained
	// The agent gave its verdict and finished while no worker watched.
	f.orca.finish("term_1", "passed")
	f.orca.mu.Lock()
	f.orca.agents["tab:1"].Interrupted = false
	f.orca.mu.Unlock()
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleAccept))
	if p := f.orca.prompt("tab:1"); !strings.Contains(p, "submit_verdict") {
		t.Fatalf("prompt = %q, want the verdict asked for again", p)
	}
	f.submit(protocol.VerdictPassed)
	f.orca.finish("term_1", "passed again")
	if res, err := w.Wait(); err != nil || res.Verdict == nil || res.Verdict.Verdict != protocol.VerdictPassed {
		t.Fatalf("result = %+v, %v", res, err)
	}
	<-drained
}

func TestOrcaWorkerAsksOnceForAMissingVerdict(t *testing.T) {
	f := newWorkerFixture(t)
	w, _, drained := f.start(context.Background(), f.spec(protocol.RoleAccept))
	f.orca.finish("term_1", "reviewed")
	// Asked once: the agent answers without a verdict again.
	for f.orca.prompt("tab:1") != harness.VerdictReminder {
		time.Sleep(time.Millisecond)
	}
	f.orca.finish("term_1", "still nothing")
	res, err := w.Wait()
	<-drained
	if err != nil || res.Verdict != nil || res.Summary != "still nothing" {
		t.Fatalf("result = %+v, %v; want no verdict after one reminder", res, err)
	}
}

func TestOrcaWorkerContinuesALateInterruption(t *testing.T) {
	f := newWorkerFixture(t)
	f.orca.lateEsc = true
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrPaused))
	w.Wait()
	<-drained
	if !f.state().Interrupted {
		t.Fatalf("state %+v, want the interruption journaled", f.state())
	}
	// Resumed before orca reports the interruption: the agent still looks
	// busy.
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	f.orca.interrupt("term_1")
	for !strings.Contains(f.orca.prompt("tab:1"), "interrupted") {
		time.Sleep(time.Millisecond)
	}
	f.orca.finish("term_1", "continued")
	if res, err := w.Wait(); err != nil || res.Summary != "continued" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	<-drained
}

func TestFault_ContinuationReplyLost(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrPaused))
	w.Wait()
	<-drained
	f.orca.loseSend = 1
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--retry-request") == "req-1" }); n != 1 {
		t.Fatalf("%d replays, want the lost send replayed", n)
	}
	if !slices.ContainsFunc(f.saves, func(s workerState) bool { return s.Send == "req-1" }) || f.state().Send != "" {
		t.Errorf("journaled %+v, want the request recorded and then cleared", f.saves)
	}
	f.orca.finish("term_1", "ok")
	w.Wait()
	<-drained

	// A later attempt replays a send an earlier one lost.
	st := f.state()
	// It sent the continuation, lost the answer, then interrupted the
	// agent as it stopped.
	st.Send, st.SendText, st.Interrupted = "req-1", f.orca.prompt("tab:1"), true
	b, _ := json.Marshal(st)
	f.thread = string(b)
	f.orca.interrupt("term_1")
	sends := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--retry-request") == "req-1" })
	w, _, drained = f.start(context.Background(), f.spec(protocol.RoleImplement))
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--retry-request") == "req-1" }); n != sends+1 {
		t.Errorf("%d replays, want the journaled request replayed", n-sends)
	}
	f.orca.finish("term_1", "ok")
	w.Wait()
	<-drained
}

func TestFault_StoppedDuringLaunch(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	f.orca.onLaunch = func() error {
		// Paused while orca takes the launch; its answer never comes.
		cancel(fmt.Errorf("runner: %w", harness.ErrPaused))
		return &Error{Code: "runtime_timeout", Message: "no answer"}
	}
	if _, err := f.ws.Start(ctx, f.spec(protocol.RoleImplement)); !errors.Is(err, harness.ErrPaused) {
		t.Fatalf("start = %v, want the pause", err)
	}
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--terminal") == "term_1" && flag(c, "--text") == esc }); n != 1 {
		t.Errorf("%d escapes, want the launched agent found and interrupted", n)
	}
	if st := f.state(); st.Terminal != "term_1" || !st.Interrupted {
		t.Errorf("state %+v, want the agent recorded for the next attempt", st)
	}
}

func TestReleaseClosesTheAgent(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrPaused))
	w.Wait()
	<-drained
	if err := f.ws.Release(context.Background(), "run_1", f.thread); err != nil {
		t.Fatal(err)
	}
	// Again, as after a restart; and for a thread from another harness.
	if err := f.ws.Release(context.Background(), "run_1", f.thread); err != nil {
		t.Fatal(err)
	}
	if err := f.ws.Release(context.Background(), "run_1", "thr_traex"); err != nil {
		t.Fatal(err)
	}
	if n := f.orca.count("terminal close", func(c []string) bool { return flag(c, "--terminal") == "term_1" }); n != 1 {
		t.Errorf("closed %d times, want once", n)
	}

	// A launch never seen answered is found by its marker.
	if _, err := f.orca.exec(context.Background(), f.dir, []string{"agent", "launch", "--prompt", "[tend run run_1, launch lch_x]"}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(workerState{Worktree: f.orca.wt, Marker: "lch_x"})
	if err := f.ws.Release(context.Background(), "run_1", string(b)); err != nil {
		t.Fatal(err)
	}
	if n := f.orca.count("terminal close", func(c []string) bool { return flag(c, "--terminal") == "term_2" }); n != 1 {
		t.Errorf("closed the lost launch's terminal %d times, want once", n)
	}
}

func TestFault_LaunchLookupFails(t *testing.T) {
	f := newWorkerFixture(t)
	// An earlier attempt's launch is unknown and has had time to settle,
	// but orca cannot be read: that is no proof the agent is absent.
	b, _ := json.Marshal(workerState{Worktree: f.orca.wt, Marker: "lch_x", Asked: now() - time.Hour.Milliseconds()})
	f.thread = string(b)
	f.orca.failPs = true
	if _, err := f.ws.Start(context.Background(), f.spec(protocol.RoleImplement)); err == nil {
		t.Fatal("start succeeded without seeing orca")
	}
	if n := f.orca.count("agent launch", nil); n != 0 || f.state().Marker != "lch_x" {
		t.Errorf("%d launches, state %+v; want the launch kept unknown", n, f.state())
	}
	if err := f.ws.Release(context.Background(), "run_1", f.thread); err == nil {
		t.Error("release succeeded without seeing orca")
	}
}

// owner types into the agent's terminal as the owner would in orca.
func (f *liveOrca) owner(handle string, args ...string) {
	if _, err := f.exec(context.Background(), "", append([]string{"terminal", "send", "--terminal", handle}, args...)); err != nil {
		panic(err)
	}
}

func TestOrcaWorkerNoticesTheOwnersPrompt(t *testing.T) {
	f := newWorkerFixture(t)
	taken := make(chan string, 1)
	spec := f.spec(protocol.RoleImplement)
	spec.OnTakeover = func(reason string) error { taken <- reason; return nil }
	ctx, cancel := context.WithCancelCause(context.Background())
	w, events, drained := f.start(ctx, spec)
	f.orca.owner("term_1", "--text", "use the other flag parser", "--enter")
	if reason := <-taken; !strings.Contains(reason, "prompt tend did not give") {
		t.Fatalf("reason = %q", reason)
	}
	// The owner's turn ends: that is not the worker's to judge.
	f.orca.finish("term_1", "switched parsers")
	cancel(fmt.Errorf("runner: %w", harness.ErrHeld))
	if _, err := w.Wait(); !errors.Is(err, harness.ErrHeld) {
		t.Fatalf("wait = %v, want held", err)
	}
	<-drained
	if !slices.Contains(kinds(*events), "worker.taken_over") || slices.Contains(kinds(*events), "turn.completed") {
		t.Errorf("events = %q", kinds(*events))
	}
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--text") == esc }); n != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatalf("%d escapes, %d closes; want the agent left to the owner", n, f.orca.count("terminal close", nil))
	}
	// tend resume: the agent gets the hand-back turn and finishes.
	spec = f.spec(protocol.RoleImplement)
	spec.HandBack = true
	w, _, drained = f.start(context.Background(), spec)
	if p := f.orca.prompt("tab:1"); !strings.Contains(p, "handed the task back") {
		t.Fatalf("prompt = %q, want the hand-back", p)
	}
	f.orca.finish("term_1", "finished with the owner's parser")
	if res, err := w.Wait(); err != nil || res.Summary != "finished with the owner's parser" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	<-drained
}

func TestOrcaWorkerNoticesTheOwnersInterruption(t *testing.T) {
	f := newWorkerFixture(t)
	taken := make(chan string, 1)
	spec := f.spec(protocol.RoleImplement)
	spec.OnTakeover = func(reason string) error { taken <- reason; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	w, _, drained := f.start(ctx, spec)
	f.orca.owner("term_1", "--text", esc)
	if reason := <-taken; !strings.Contains(reason, "interrupted") {
		t.Fatalf("reason = %q", reason)
	}
	// Even a shutdown leaves a taken-over agent alone.
	cancel()
	w.Wait()
	<-drained
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--text") == esc }); n != 1 {
		t.Errorf("%d escapes, want only the owner's", n)
	}
}

func TestOrcaWorkerNoticesATakeoverWhilePaused(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrPaused))
	w.Wait()
	<-drained
	// While paused, the owner works in the agent; then resumes the task.
	f.orca.owner("term_1", "--text", "look at the failing test first", "--enter")
	taken := make(chan string, 1)
	spec := f.spec(protocol.RoleImplement)
	spec.OnTakeover = func(reason string) error { taken <- reason; return nil }
	ctx, cancel = context.WithCancelCause(context.Background())
	w, _, drained = f.start(ctx, spec)
	<-taken
	if p := f.orca.prompt("tab:1"); p != "look at the failing test first" {
		t.Errorf("prompt = %q, want the owner's left alone", p)
	}
	cancel(fmt.Errorf("runner: %w", harness.ErrHeld))
	w.Wait()
	<-drained
}

func TestOrcaWorkerHandsBackAfterAPendingTurn(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrHeld))
	w.Wait()
	<-drained
	// A continuation's answer was lost before the owner took over.
	st := f.state()
	st.Send, st.SendText = "req-1", "go on"
	b, _ := json.Marshal(st)
	f.thread = string(b)
	spec := f.spec(protocol.RoleImplement)
	spec.HandBack = true
	w, _, drained = f.start(context.Background(), spec)
	if n := f.orca.count("terminal send", func(c []string) bool { return flag(c, "--retry-request") == "req-1" }); n != 1 {
		t.Errorf("%d replays, want the pending turn settled", n)
	}
	if p := f.orca.prompt("tab:1"); !strings.Contains(p, "handed the task back") {
		t.Fatalf("prompt = %q, want the hand-back as its own turn", p)
	}
	f.orca.finish("term_1", "done")
	w.Wait()
	<-drained
}

func TestOrcaWorkerReplaysAPendingHandBackOnce(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, _, drained := f.start(ctx, f.spec(protocol.RoleImplement))
	cancel(fmt.Errorf("runner: %w", harness.ErrHeld))
	w.Wait()
	<-drained
	spec := f.spec(protocol.RoleImplement)
	spec.HandBack = true
	// The hand-back went out, but its answer was lost before the runner
	// could clear the hold.
	f.orca.owner("term_1", "--text", harness.HandBack(spec), "--enter")
	st := f.state()
	st.Send, st.SendText, st.Said = "req-1", harness.HandBack(spec), said(harness.HandBack(spec))
	b, _ := json.Marshal(st)
	f.thread = string(b)
	spec = f.spec(protocol.RoleImplement)
	spec.HandBack = true
	sends := f.orca.count("terminal send", nil)
	w, _, drained = f.start(context.Background(), spec)
	if n := f.orca.count("terminal send", nil) - sends; n != 1 {
		t.Errorf("%d sends, want only the replay of the hand-back", n)
	}
	f.orca.finish("term_1", "done")
	w.Wait()
	<-drained
}
