package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerOptions configure the TUI workers a runner starts in orca (D66).
type WorkerOptions struct {
	Client *Client
	// Agent is the agent to launch: codex or traex.
	Agent protocol.AgentKind
	// Model and Effort override the agent's defaults when set; orca takes
	// an effort only with a model.
	Model  string
	Effort string
	// BridgeCommand and BridgeArgs start the runner's worker-mcp
	// subcommand, which serves the run's tools to the agent.
	BridgeCommand string
	BridgeArgs    []string
	// SocketDir holds the runs' bridge sockets. Default os.TempDir().
	SocketDir string
	IDs       protocol.IDGen
	Now       func() time.Time
	Log       *slog.Logger
	// Settle is how long a launch whose outcome is unknown may take to
	// show up in orca, and how long orca may go without any state for a
	// running agent before the worker gives up on it. Default 2m.
	Settle time.Duration
	// Poll is how often the agent's state is read while waiting. Default
	// 5s.
	Poll time.Duration
	// WaitChunk bounds each wait for the terminal to go idle. Default 1m.
	WaitChunk time.Duration
}

// Workers implements harness.Workers with agent TUIs in orca terminals.
// The agent lives in orca, not in the runner: it outlives a worker that
// stops for a pause or a restart, and the next attempt of the run picks it
// up again from the state the worker journals as its thread.
type Workers struct {
	opts WorkerOptions
}

var _ harness.Workers = (*Workers)(nil)

// NewWorkers returns Workers.
func NewWorkers(opts WorkerOptions) *Workers {
	if opts.SocketDir == "" {
		opts.SocketDir = os.TempDir()
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Settle == 0 {
		opts.Settle = 2 * time.Minute
	}
	if opts.Poll == 0 {
		opts.Poll = 5 * time.Second
	}
	if opts.WaitChunk == 0 {
		opts.WaitChunk = time.Minute
	}
	return &Workers{opts: opts}
}

var _ harness.Releaser = (*Workers)(nil)

// Release implements harness.Releaser: the agent of a run tend gives up
// stops with it, whether or not a worker still watches it.
func (ws *Workers) Release(ctx context.Context, runID, thread string) error {
	var st workerState
	if json.Unmarshal([]byte(thread), &st) != nil || st.Worktree == "" {
		return nil
	}
	terms, err := ws.opts.Client.Terminals(ctx, "id:"+st.Worktree)
	if err != nil {
		return err
	}
	panes := map[string]string{}
	for _, t := range terms {
		panes[t.PaneKey()] = t.Handle
	}
	handle := ""
	switch {
	case st.Terminal != "":
		if _, ok := panes[st.Pane]; ok {
			handle = st.Terminal
		}
	case st.Marker != "":
		// A launch whose answer was never seen: its agent may show up
		// until the launch has settled, and the run is forgotten after
		// this.
		for {
			handle, _, err = ws.byMarker(ctx, st.Worktree, st.Marker)
			if err != nil {
				return err
			}
			if handle != "" || ws.opts.Now().UnixMilli() >= st.Asked+ws.opts.Settle.Milliseconds() {
				break
			}
			if err := sleep(ctx, ws.opts.Poll); err != nil {
				return err
			}
		}
	}
	if handle == "" {
		return nil
	}
	ws.opts.Log.InfoContext(ctx, "closing the agent of a run tend gave up", "run_id", runID, "terminal", handle)
	return ws.opts.Client.CloseTerminal(ctx, handle)
}

// orcaAgents are orca's names for the agents a worker can be.
var orcaAgents = map[protocol.AgentKind]string{
	protocol.AgentCodex: "codex",
	protocol.AgentTraex: "trae",
}

// workerState is what the worker journals as its thread, so that the next
// attempt finds the agent again.
type workerState struct {
	// Marker is in the brief of the agent's launch; it finds the agent
	// when the runner stopped before the launch was answered.
	Marker string `json:"marker,omitempty"`
	// Asked is when that launch was asked for, in Unix milliseconds.
	Asked int64 `json:"asked,omitempty"`
	// Launch is orca's operation for a launch whose answer was lost.
	Launch string `json:"launch,omitempty"`
	// Terminal and Pane identify the agent once it runs.
	Terminal string `json:"terminal,omitempty"`
	Pane     string `json:"pane,omitempty"`
	// Turn is when tend's latest turn began, in Unix milliseconds; an
	// agent done since then has answered it.
	Turn int64 `json:"turn,omitempty"`
	// Worktree is the orca worktree's ID, for Release.
	Worktree string `json:"worktree,omitempty"`
	// Send is orca's request for a turn whose answer was lost, and
	// SendText its text; the same turn is replayed under it rather than
	// sent again.
	Send     string `json:"send,omitempty"`
	SendText string `json:"send_text,omitempty"`
	// Interrupted says tend interrupted the agent's turn and has not
	// continued it yet; orca may report the interruption late.
	Interrupted bool `json:"interrupted,omitempty"`
	// Said is the start of tend's latest prompt; an agent working on
	// another one was given it by someone else.
	Said string `json:"said,omitempty"`
}

// Agent states as orca reports them.
const (
	stateWorking = "working"
	stateDone    = "done"
)

// esc interrupts the agent's turn and keeps it running.
const esc = "\x1b"

type worker struct {
	ws     *Workers
	spec   harness.WorkerSpec
	st     workerState
	wt     string // the orca worktree's ID
	events chan harness.WorkerEvent
	done   chan struct{}
	res    harness.WorkerResult
	err    error

	srv      *bridge.Server
	verdicts harness.Verdicts
	// holding is why the worker leaves the agent to the owner, once it
	// noticed a takeover.
	holding string
	// askedVerdict is set once an acceptance agent that finished without
	// a verdict this worker received was asked for it again.
	askedVerdict bool
	state        string // the agent state last reported
	// unseen is when the worker first found the agent's terminal with no
	// state for the agent in orca.
	unseen time.Time
	seq    int
	// pending holds the facts of starting, until run delivers them.
	pending []harness.WorkerEvent
	running bool
}

func (w *worker) Events() <-chan harness.WorkerEvent { return w.events }

func (w *worker) Wait() (harness.WorkerResult, error) {
	<-w.done
	return w.res, w.err
}

// Start implements harness.Workers.
func (ws *Workers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	agent, ok := orcaAgents[ws.opts.Agent]
	if !ok {
		return nil, fmt.Errorf("orca: agent %q cannot be a worker", ws.opts.Agent)
	}
	wt, err := ws.opts.Client.Worktree(ctx, "path:"+spec.Dir)
	if err != nil {
		return nil, fmt.Errorf("orca: find the worker's worktree: %w", err)
	}
	w := &worker{ws: ws, spec: spec, wt: wt.ID, events: make(chan harness.WorkerEvent), done: make(chan struct{})}
	// A thread from another harness, such as a traex thread ID from
	// before the host moved to orca, does not decode and starts over.
	_ = json.Unmarshal([]byte(spec.Thread), &w.st)
	if err := w.openBridge(ctx); err != nil {
		return nil, err
	}
	if err := w.begin(ctx, agent); err != nil {
		w.closeBridge(ctx)
		if cause := context.Cause(ctx); cause != nil {
			// A turn, or a launch, may have started before the worker was
			// stopped.
			w.stop(ctx, cause)
			return nil, cause
		}
		return nil, err
	}
	go w.run(ctx)
	return w, nil
}

// openBridge serves the run's tools to the agent and points the agent's
// configuration at them (D66). The worktree's marker also keeps the
// owner's tend add-on out of the agent.
func (w *worker) openBridge(ctx context.Context) error {
	var tools []harness.ToolSpec
	if w.spec.Role == protocol.RoleAccept {
		tools = []harness.ToolSpec{harness.VerdictToolSpec}
	}
	sock := filepath.Join(w.ws.opts.SocketDir, "tend-"+w.spec.RunID+".sock")
	token := w.ws.opts.IDs.New(protocol.PrefixToken)
	srv, err := bridge.Listen(sock, token, tools, w.verdicts.Handle)
	if err != nil {
		return err
	}
	st := bridge.State{Run: w.spec.RunID, Socket: sock, Token: token}
	if err := bridge.Install(ctx, w.spec.Dir, w.ws.opts.Agent, w.ws.opts.BridgeCommand, w.ws.opts.BridgeArgs, st); err != nil {
		_ = srv.Close()
		return err
	}
	w.srv = srv
	return nil
}

func (w *worker) closeBridge(ctx context.Context) {
	_ = w.srv.Close()
	if err := bridge.Uninstall(context.WithoutCancel(ctx), w.spec.Dir, w.ws.opts.Agent); err != nil {
		w.ws.opts.Log.WarnContext(ctx, "could not remove the worker's tool bridge", "run_id", w.spec.RunID, "err", err)
	}
}

// save journals the worker's state before anything it describes happens.
func (w *worker) save() error {
	if w.spec.OnThread == nil {
		return nil
	}
	b, err := json.Marshal(w.st)
	if err != nil {
		return err
	}
	return w.spec.OnThread(string(b))
}

// begin leaves the agent with a turn of tend's under way: the one an
// earlier attempt left running, a continuation, or a new agent's brief.
func (w *worker) begin(ctx context.Context, agent string) error {
	if w.st.Terminal != "" {
		a, alive, err := w.agent(ctx)
		if err != nil {
			return err
		}
		if alive && w.spec.HandBack {
			// The owner handed the run back (D67). A turn whose answer
			// was lost is settled first, so the hand-back is a turn of
			// its own.
			handBack := harness.HandBack(w.spec)
			if w.st.Send != "" {
				pending := w.st.SendText
				if err := w.send(ctx, ""); err != nil || pending == handBack {
					return err // the hand-back itself was pending
				}
			}
			return w.send(ctx, handBack)
		}
		if alive {
			if reason := w.takenOver(a); reason != "" {
				w.holding = reason // run reports it and waits
				return nil
			}
			// A verdict from an earlier attempt is not journaled: an
			// acceptance agent done meanwhile is asked to give it again.
			doneMeanwhile := answered(a, w.st.Turn) && w.spec.Role != protocol.RoleAccept
			if (a.State == "" || a.State == stateWorking || doneMeanwhile) && w.st.Send == "" {
				// In, or done with, the turn an earlier attempt started. If
				// tend interrupted it and orca has not said so yet, step
				// continues it once it does.
				return nil
			}
			// A turn whose answer was lost is replayed as it was sent.
			return w.send(ctx, harness.Continuation(w.spec))
		}
		w.ws.opts.Log.InfoContext(ctx, "the worker's agent is gone; launching a new one with the brief", "run_id", w.spec.RunID, "terminal", w.st.Terminal)
		w.st = workerState{}
	}
	if w.st.Marker != "" && w.st.Launch == "" {
		// The runner stopped before the launch was answered (C4: its
		// outcome is unknown until orca shows the agent or the launch has
		// had time to settle).
		found, err := w.recover(ctx)
		if err != nil || found {
			return err
		}
		w.st = workerState{}
	}
	return w.launch(ctx, agent)
}

// prompt is the brief a new agent starts with. A TUI has no separate
// developer instructions, so they lead the brief, after the marker.
func (w *worker) prompt() string {
	instructions, input := harness.Brief(w.spec)
	return "[tend run " + w.spec.RunID + ", launch " + w.st.Marker + "]\n\n" + instructions + "\n\n" + input
}

// launchAttempts bounds how often a launch whose answer was lost is
// replayed in one attempt.
const launchAttempts = 5

func (w *worker) launch(ctx context.Context, agent string) error {
	if w.st.Marker == "" {
		w.st.Worktree = w.wt
		w.st.Marker = w.ws.opts.IDs.New(protocol.PrefixLaunch)
		w.st.Asked = w.ws.opts.Now().UnixMilli()
		w.st.Turn = w.st.Asked
		if err := w.save(); err != nil {
			return err
		}
	}
	prompt := w.prompt()
	if w.st.Said != said(prompt) {
		w.st.Said = said(prompt)
		if err := w.save(); err != nil {
			return err
		}
	}
	for i := 0; ; i++ {
		l, err := w.ws.opts.Client.LaunchAgent(ctx, LaunchOptions{
			Dir: w.spec.Dir, Agent: agent, Worktree: "id:" + w.wt, Prompt: prompt,
			Model: w.ws.opts.Model, Effort: w.ws.opts.Effort, RetryLaunch: w.st.Launch,
		})
		if err == nil {
			return w.launched(ctx, l, prompt)
		}
		if id := LaunchRetryID(err); id != "" {
			w.st.Launch = id
			if serr := w.save(); serr != nil {
				return serr
			}
			if i+1 < launchAttempts && ctx.Err() == nil {
				if err := sleep(ctx, w.ws.opts.Poll); err != nil {
					return err
				}
				continue
			}
			return err // the next attempt replays it
		}
		if !Unconfirmed(err) {
			// orca refused the launch: nothing started.
			w.st = workerState{}
			if serr := w.save(); serr != nil {
				return serr
			}
		}
		return fmt.Errorf("orca: launch the worker: %w", err)
	}
}

func (w *worker) launched(ctx context.Context, l Launch, prompt string) error {
	if l.Outcome.Kind != OutcomeTerminal {
		if l.Outcome.Handle != "" {
			_ = w.ws.opts.Client.CloseTerminal(context.WithoutCancel(ctx), l.Outcome.Handle)
		}
		w.st = workerState{}
		_ = w.save()
		return fmt.Errorf("orca started the %s worker as a chat (%s); turn off the Chat UI for %s in orca on this host", w.ws.opts.Agent, l.Receipt.Detail, w.ws.opts.Agent)
	}
	w.st.Launch = ""
	w.st.Terminal, w.st.Pane = l.Outcome.Handle, l.Outcome.PaneKey
	if err := w.save(); err != nil {
		return err
	}
	w.emit(ctx, "turn.started", map[string]string{"prompt": prompt})
	return nil
}

// recover looks for the agent of a launch the runner did not see answered,
// until the launch has had Settle to show up.
func (w *worker) recover(ctx context.Context) (bool, error) {
	for {
		// A failed look is not an absence: the launch stays unknown.
		h, pane, err := w.ws.byMarker(ctx, w.wt, w.st.Marker)
		if err != nil {
			return false, err
		}
		if h != "" {
			w.st.Terminal, w.st.Pane = h, pane
			return true, w.save()
		}
		if w.ws.opts.Now().UnixMilli() >= w.st.Asked+w.ws.opts.Settle.Milliseconds() {
			w.ws.opts.Log.InfoContext(ctx, "the worker's launch did not show up in orca; launching again", "run_id", w.spec.RunID, "marker", w.st.Marker)
			return false, nil
		}
		if err := sleep(ctx, w.ws.opts.Poll); err != nil {
			return false, err
		}
	}
}

// findWait bounds how long a stopping worker looks for the agent of a
// launch it did not see answered.
const findWait = 10 * time.Second

// findLaunched looks for the agent of the launch under way, for a while,
// and records it when found.
func (w *worker) findLaunched(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, findWait)
	defer cancel()
	for {
		if h, pane, _ := w.ws.byMarker(ctx, w.wt, w.st.Marker); h != "" {
			w.st.Terminal, w.st.Pane, w.st.Launch = h, pane, ""
			_ = w.save()
			return
		}
		if sleep(ctx, w.ws.opts.Poll) != nil {
			return
		}
	}
}

// byMarker finds the terminal of the agent launched with marker.
func (ws *Workers) byMarker(ctx context.Context, wt, marker string) (handle, pane string, err error) {
	agents, err := ws.opts.Client.Agents(ctx, wt)
	if err != nil {
		return "", "", err
	}
	terms, err := ws.opts.Client.Terminals(ctx, "id:"+wt)
	if err != nil {
		return "", "", err
	}
	for _, a := range agents {
		if !strings.Contains(a.Prompt, marker) {
			continue
		}
		for _, t := range terms {
			if t.PaneKey() == a.PaneKey {
				return t.Handle, a.PaneKey, nil
			}
		}
	}
	return "", "", nil
}

// sendAttempts bounds how often a turn whose answer was lost is replayed.
const sendAttempts = 3

// send starts a turn with text in the agent's terminal. A turn whose answer
// an earlier attempt lost is replayed instead, with the text and the turn
// it had: orca binds the request to its text, and may have run it
// already.
func (w *worker) send(ctx context.Context, text string) error {
	if w.st.Send != "" {
		text = w.st.SendText
	} else {
		w.st.Turn = w.ws.opts.Now().UnixMilli()
		w.st.Interrupted, w.st.Said = false, said(text)
		if err := w.save(); err != nil {
			return err
		}
	}
	o := SendOptions{Terminal: w.st.Terminal, Text: text, Enter: true, WaitSubmit: 30 * time.Second, RetryRequest: w.st.Send}
	for i := 0; ; i++ {
		_, err := w.ws.opts.Client.SendInput(ctx, o)
		if err == nil || !Unconfirmed(err) {
			w.st.Send, w.st.SendText = "", ""
			if serr := w.save(); serr != nil {
				return serr
			}
		}
		if err == nil {
			w.emit(ctx, "turn.started", map[string]string{"prompt": text})
			return nil
		}
		if id := SendRetryID(err); id != "" && id != w.st.Send {
			w.st.Send, w.st.SendText = id, text
			if serr := w.save(); serr != nil {
				return serr
			}
		}
		if w.st.Send == "" || i+1 == sendAttempts || ctx.Err() != nil {
			return fmt.Errorf("orca: send the worker a turn: %w", err)
		}
		o.RetryRequest = w.st.Send
	}
}

// agent returns the worker's agent and whether its terminal still runs.
func (w *worker) agent(ctx context.Context) (Agent, bool, error) {
	terms, err := w.ws.opts.Client.Terminals(ctx, "id:"+w.wt)
	if err != nil {
		return Agent{}, false, err
	}
	alive := false
	for _, t := range terms {
		alive = alive || t.Handle == w.st.Terminal
	}
	if !alive {
		return Agent{}, false, nil
	}
	agents, err := w.ws.opts.Client.Agents(ctx, w.wt)
	if err != nil {
		return Agent{}, false, err
	}
	for _, a := range agents {
		if a.PaneKey == w.st.Pane {
			return a, true, nil
		}
	}
	return Agent{}, true, nil // orca has not seen the agent's first state yet
}

// run waits for the agent to finish tend's turn.
func (w *worker) run(ctx context.Context) {
	defer close(w.done)
	defer close(w.events)
	defer w.closeBridge(ctx)
	w.running = true
	for _, ev := range w.pending {
		select {
		case w.events <- ev:
		case <-ctx.Done():
		}
	}
	w.pending = nil
	for {
		if w.holding != "" {
			w.hold(ctx)
			return
		}
		a, done, err := w.step(ctx)
		if cause := context.Cause(ctx); cause != nil {
			w.stop(ctx, cause)
			w.err = cause
			return
		}
		if err != nil {
			w.err = err
			return
		}
		if done {
			w.emit(ctx, "turn.completed", map[string]string{"reply": a.LastAssistantMessage})
			if w.spec.Role == protocol.RoleAccept && w.verdicts.Verdict() == nil && !w.askedVerdict {
				// The verdict may have gone to an earlier worker, or never
				// been given: ask once more before calling it inconclusive.
				w.askedVerdict = true
				if err := w.send(ctx, harness.VerdictReminder); err != nil && ctx.Err() == nil {
					w.err = err
					return
				}
				continue
			}
			w.res = harness.WorkerResult{Verdict: w.verdicts.Verdict(), Summary: a.LastAssistantMessage}
			return
		}
	}
}

// failuresAllowed is how many reads of orca in a row may fail before the
// worker gives up.
const failuresAllowed = 5

// step waits a while for the agent and reports whether it finished tend's
// turn.
func (w *worker) step(ctx context.Context) (Agent, bool, error) {
	var lastErr error
	for range failuresAllowed {
		wait, err := w.ws.opts.Client.WaitFor(ctx, w.st.Terminal, WaitTUIIdle, w.ws.opts.WaitChunk)
		if oe, ok := errors.AsType[*Error](err); ok && oe.Code == codeTimeout {
			err = nil // still busy after a whole chunk
		}
		if err == nil && wait.ExitCode != nil {
			return Agent{}, false, fmt.Errorf("orca: the worker's agent exited with code %d", *wait.ExitCode)
		}
		var a Agent
		alive := true
		if err == nil {
			a, alive, err = w.agent(ctx)
		}
		if ctx.Err() != nil {
			return Agent{}, false, nil
		}
		if err != nil {
			lastErr = err
			if err := sleep(ctx, w.ws.opts.Poll); err != nil {
				return Agent{}, false, nil
			}
			continue
		}
		if !alive {
			return Agent{}, false, errors.New("orca: the worker's terminal was closed")
		}
		if a.PaneKey == "" {
			// orca learns an agent's state from the agent's hooks; without
			// them it never sees a turn end.
			if w.unseen.IsZero() {
				w.unseen = w.ws.opts.Now()
			} else if w.ws.opts.Now().Sub(w.unseen) >= w.ws.opts.Settle {
				return Agent{}, false, fmt.Errorf("orca: no state for the worker's %s agent after %s; check that its hooks for orca are enabled and trusted on this host",
					w.ws.opts.Agent, w.ws.opts.Settle)
			}
		} else {
			w.unseen = time.Time{}
		}
		if a.State != "" && a.State != w.state {
			w.state = a.State
			w.emit(ctx, "agent.state", map[string]string{"state": a.State, "blocked_reason": wait.BlockedReason})
		}
		if reason := w.takenOver(a); reason != "" {
			w.holding = reason
			return a, false, nil
		}
		if wait.Satisfied && answered(a, w.st.Turn) {
			return a, true, nil
		}
		if w.st.Interrupted && a.State == stateDone && a.Interrupted {
			// tend's own interruption, reported only now.
			return a, false, w.send(ctx, harness.Continuation(w.spec))
		}
		// Idle but not done with this turn (orca may not have seen it end),
		// or waiting for someone, which orca reports at once: look again
		// later.
		if err := sleep(ctx, w.ws.opts.Poll); err != nil {
			return Agent{}, false, nil
		}
		return a, false, nil
	}
	return Agent{}, false, fmt.Errorf("orca: lost track of the worker: %w", lastErr)
}

// takenOver says why the agent is no longer in tend's hands, or "" while
// it is: someone gave it a prompt tend did not, or interrupted a turn tend
// did not interrupt (D67).
func (w *worker) takenOver(a Agent) string {
	if a.StateStartedAt < w.st.Turn || a.State == "" {
		return ""
	}
	if a.Prompt != "" && w.st.Said != "" && !samePrompt(said(a.Prompt), w.st.Said) {
		return "the agent works on a prompt tend did not give"
	}
	if a.State == stateDone && a.Interrupted && !w.st.Interrupted {
		return "someone interrupted the agent's turn"
	}
	return ""
}

// hold reports the takeover and leaves the agent to the owner until the
// worker is stopped. Whether the owner holds the run is the runner's to
// keep: it learns so from tend, and tells the next worker to hand back.
func (w *worker) hold(ctx context.Context) {
	w.emit(ctx, "worker.taken_over", map[string]string{"reason": w.holding})
	if w.spec.OnTakeover != nil {
		if err := w.spec.OnTakeover(w.holding); err != nil && ctx.Err() == nil {
			w.ws.opts.Log.WarnContext(ctx, "could not report the takeover", "run_id", w.spec.RunID, "err", err)
		}
	}
	<-ctx.Done()
	w.err = context.Cause(ctx)
}

// saidLen is how much of a prompt identifies it: orca may keep less than
// the whole of a long one.
const saidLen = 200

// said is the start of a prompt, with its spacing evened out.
func said(prompt string) string {
	s := strings.Join(strings.Fields(prompt), " ")
	if r := []rune(s); len(r) > saidLen {
		s = string(r[:saidLen])
	}
	return s
}

// samePrompt reports whether two prompts' starts agree as far as both go.
func samePrompt(a, b string) bool {
	n := min(len(a), len(b))
	return a[:n] == b[:n]
}

// codeTimeout is the error code of a wait whose condition did not hold in
// time.
const codeTimeout = "timeout"

// answered reports whether the agent finished the turn tend began at turn.
// A turn someone interrupted did not finish.
func answered(a Agent, turn int64) bool {
	return a.State == stateDone && !a.Interrupted && a.StateStartedAt >= turn
}

// stop leaves the agent as the cause asks: closed when tend no longer
// wants the run, otherwise interrupted, keeping the terminal for the next
// attempt (D66).
func (w *worker) stop(ctx context.Context, cause error) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if w.st.Terminal == "" && w.st.Marker != "" {
		// The launch may have gone through without its answer: look for
		// its agent for a while.
		w.findLaunched(stopCtx)
	}
	if w.st.Terminal == "" {
		return
	}
	var err error
	switch {
	case errors.Is(cause, harness.ErrStopped):
		err = w.ws.opts.Client.CloseTerminal(stopCtx, w.st.Terminal)
	case errors.Is(cause, harness.ErrHeld), w.holding != "":
		// The owner works in the agent: leave it alone.
	default:
		w.st.Interrupted = true
		if err = w.save(); err == nil {
			_, err = w.ws.opts.Client.SendInput(stopCtx, SendOptions{Terminal: w.st.Terminal, Text: esc})
		}
	}
	if err != nil {
		w.ws.opts.Log.WarnContext(ctx, "could not stop the worker's agent", "run_id", w.spec.RunID, "terminal", w.st.Terminal, "err", err)
	}
}

// emit reports a fact unless the worker is stopping. Facts of starting
// wait for run, as nobody reads them before Start returns.
func (w *worker) emit(ctx context.Context, kind string, body any) {
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	w.seq++
	ev := harness.WorkerEvent{NativeID: fmt.Sprintf("%s@%d#%d", w.st.Terminal, w.st.Turn, w.seq), Kind: kind, Body: b}
	if !w.running {
		w.pending = append(w.pending, ev)
		return
	}
	select {
	case w.events <- ev:
	case <-ctx.Done():
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
