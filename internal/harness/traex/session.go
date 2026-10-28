package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/procx"
)

// Options start an app-server.
type Options struct {
	// Bin is the traex binary; empty means "traex" on PATH.
	Bin string
	// Env is added to the inherited environment, for example TRAE_HOME
	// for a dedicated home. Orca context is removed after merging.
	Env []string
	// Dir is the process's working directory.
	Dir string
	// Stderr receives the app-server's diagnostics; nil discards them.
	Stderr io.Writer
	// Version is reported as the client version.
	Version string
	Log     *slog.Logger
}

// Session is a running app-server. It implements harness.Session.
type Session struct {
	conn *conn
	log  *slog.Logger
	stop func() error

	mu      sync.Mutex
	threads map[string]*thread
	// threadLifecycle serializes Resume and Release without blocking event delivery.
	threadLifecycle chan struct{}
	// orphans holds events of threads not registered yet, such as a new
	// thread whose start response is still on its way to the caller.
	orphans map[string][]harness.Event
	ended   bool
	// stopped is closed by Close, releasing event pumps whose consumer
	// went away.
	stopped chan struct{}
	closed  sync.Once
}

// maxOrphans bounds the events kept for threads not registered yet.
const maxOrphans = 1000

var _ harness.Session = (*Session)(nil)

// Start launches `traex app-server` and completes the handshake.
func Start(ctx context.Context, opts Options) (*Session, error) {
	bin := opts.Bin
	if bin == "" {
		bin = "traex"
	}
	procCtx, kill := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, bin, "app-server")
	cmd.Dir = opts.Dir
	cmd.Env = appServerEnv(os.Environ(), opts.Env)
	cmd.Stderr = opts.Stderr
	procx.Contain(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		kill()
		return nil, fmt.Errorf("traex: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		kill()
		return nil, fmt.Errorf("traex: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		kill()
		return nil, fmt.Errorf("traex: start app-server: %w", err)
	}
	s := newSession(stdout, stdin, opts.Log)
	s.stop = func() error {
		_ = stdin.Close()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		var err error
		select {
		case err = <-exited:
		case <-time.After(5 * time.Second):
			kill()
			err = <-exited
		}
		// Whatever the app-server started and left running, such as a
		// background command of a worker, must not outlive the session.
		procx.KillGroup(cmd)
		kill()
		return err
	}
	if err := s.initialize(ctx, opts.Version); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func appServerEnv(inherited, overrides []string) []string {
	env := make([]string, 0, len(inherited)+len(overrides))
	for _, source := range [][]string{inherited, overrides} {
		for _, entry := range source {
			key, _, _ := strings.Cut(entry, "=")
			// Independent sessions must not report hooks as their parent Orca pane.
			if !strings.HasPrefix(strings.ToUpper(key), "ORCA_") {
				env = append(env, entry)
			}
		}
	}
	return env
}

func newSession(r io.Reader, w io.WriteCloser, log *slog.Logger) *Session {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Session{log: log, threads: map[string]*thread{}, orphans: map[string][]harness.Event{}, stopped: make(chan struct{}), threadLifecycle: make(chan struct{}, 1)}
	s.conn = newConn(r, w, handlers{request: s.serverRequest, notify: s.notification}, log)
	s.stop = w.Close
	go func() {
		<-s.conn.done
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ended = true
		for _, t := range s.threads {
			t.close()
		}
	}()
	return s
}

func (s *Session) initialize(ctx context.Context, version string) error {
	if version == "" {
		version = "dev"
	}
	err := s.conn.call(ctx, methodInitialize, InitializeParams{
		ClientInfo:   ClientInfo{Name: "tend", Version: version},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: true},
	}, nil)
	if err != nil {
		return err
	}
	return s.conn.notify(ctx, methodInitialized, nil)
}

// Done implements harness.Session.
func (s *Session) Done() <-chan struct{} { return s.conn.done }

// Close implements harness.Session.
func (s *Session) Close() error {
	var err error
	s.closed.Do(func() {
		close(s.stopped)
		err = s.stop()
	})
	return err
}

func dynamicTools(specs []harness.ToolSpec) []DynamicToolSpec {
	out := make([]DynamicToolSpec, 0, len(specs))
	for _, t := range specs {
		out = append(out, DynamicToolSpec{Type: dynamicToolTypeFunc, Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// StartThread implements harness.Session.
func (s *Session) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	var res ThreadStartResponse
	err := s.conn.call(ctx, methodThreadStart, ThreadStartParams{
		Model: cfg.Model, Cwd: cfg.Cwd, ApprovalPolicy: cfg.ApprovalPolicy, ApprovalsReviewer: cfg.ApprovalsReviewer, Sandbox: cfg.Sandbox,
		DeveloperInstructions: cfg.DeveloperInstructions, Config: cfg.Config, DynamicTools: dynamicTools(cfg.Tools),
	}, &res)
	if err != nil {
		return nil, err
	}
	t := s.register(res.Thread.ID, cfg.OnToolCall)
	t.setModelDefaults(res)
	t.setPermissionDefaults(res)
	t.setNetworkPolicy(cfg, res)
	t.setRolloutPath(res.Thread.Path)
	return t, nil
}

// ResumeThread implements harness.Session.
func (s *Session) ResumeThread(ctx context.Context, threadID string, cfg harness.ThreadConfig) (harness.Thread, error) {
	unlock, err := s.lockThreadLifecycle(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Reuse the event stream of a live registration. A rejected resume must
	// neither close that stream nor replace its previously accepted handler.
	t, created := s.registerIfAbsent(threadID, cfg.OnToolCall)
	if t.toolsReleased() {
		return nil, fmt.Errorf("%w: disposable thread released", harness.ErrRejected)
	}
	var res ThreadStartResponse
	err = s.conn.call(ctx, methodThreadResume, ThreadResumeParams{
		ThreadID: threadID, Model: cfg.Model, Cwd: cfg.Cwd, ApprovalPolicy: cfg.ApprovalPolicy, ApprovalsReviewer: cfg.ApprovalsReviewer, Sandbox: cfg.Sandbox,
		DeveloperInstructions: cfg.DeveloperInstructions, Config: cfg.Config,
	}, &res)
	if err != nil {
		if created {
			s.mu.Lock()
			if s.threads[threadID] == t {
				delete(s.threads, threadID)
			}
			s.mu.Unlock()
			t.releaseLocal()
		}
		return nil, err
	}
	t.setToolHandler(cfg.OnToolCall)
	t.setModelDefaults(res)
	t.setPermissionDefaults(res)
	t.setNetworkPolicy(cfg, res)
	t.setRolloutPath(res.Thread.Path)
	return t, nil
}

// register adds or updates a thread without replacing its event stream.
func (s *Session) register(id string, onTool harness.ToolHandler) *thread {
	t, _ := s.registerIfAbsent(id, onTool)
	t.setToolHandler(onTool)
	return t
}

// registerIfAbsent preserves an existing registration while a resume is pending.
func (s *Session) registerIfAbsent(id string, onTool harness.ToolHandler) (*thread, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.threads[id]; t != nil {
		return t, false
	}
	t := newThread(s, id, onTool)
	s.threads[id] = t
	for _, ev := range s.orphans[id] {
		t.push(ev)
	}
	delete(s.orphans, id)
	if s.ended {
		t.close()
	}
	return t, true
}

func (s *Session) thread(id string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[id]
}

func (s *Session) serverRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != methodToolCall {
		// Background approval requests belong to the native auto-reviewer.
		// Unexpected interactive requests are never approved by Tend.
		return nil, fmt.Errorf("%w: %s", errUnknownMethod, method)
	}
	var p DynamicToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("traex: decode tool call: %w", err)
	}
	t := s.thread(p.ThreadID)
	var handler harness.ToolHandler
	if t != nil {
		handler = t.toolHandler()
	}
	if handler == nil {
		return toolResponse(harness.ToolResult{Text: "this thread has no tools"}), nil
	}
	call := harness.ToolCall{ThreadID: p.ThreadID, TurnID: p.TurnID, CallID: p.CallID, Tool: p.Tool, Arguments: p.Arguments}
	if p.Namespace != nil {
		call.Namespace = *p.Namespace
	}
	return toolResponse(handler(ctx, call)), nil
}

func toolResponse(r harness.ToolResult) DynamicToolCallResponse {
	return DynamicToolCallResponse{Success: r.Success, ContentItems: []ToolResponseItem{{Type: toolContentInputText, Text: r.Text}}}
}

func (s *Session) notification(method string, params json.RawMessage) {
	var ev harness.Event
	var threadID string
	switch method {
	case "thread/tokenUsage/updated":
		var n struct {
			ThreadID string `json:"threadId"`
			Context  *struct {
				Usage *ContextUsageSnapshot `json:"contextUsage"`
			} `json:"context"`
		}
		if json.Unmarshal(params, &n) != nil || n.Context == nil || n.Context.Usage == nil {
			return
		}
		usage := n.Context.Usage.usage(n.ThreadID)
		threadID = n.ThreadID
		ev = harness.Event{Kind: harness.EventContextUsage, ContextUsage: &usage}
	case notifyAgentMessageDelta:
		var n AgentMessageDeltaNotification
		if err := json.Unmarshal(params, &n); err != nil {
			s.log.Warn("undecodable assistant delta", "err", err)
			return
		}
		threadID = n.ThreadID
		ev = harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: n.TurnID, ItemID: n.ItemID,
			ItemType: harness.ItemAgentMessage, Text: n.Delta, Raw: params}
	case notifyTurnStarted, notifyTurnCompleted:
		var n TurnNotification
		if err := json.Unmarshal(params, &n); err != nil {
			s.log.Warn("undecodable turn notification", "err", err)
			return
		}
		threadID = n.ThreadID
		ev = harness.Event{Kind: harness.EventTurnStarted, TurnID: n.Turn.ID, Raw: params}
		if method == notifyTurnCompleted {
			ev.Kind, ev.TurnStatus = harness.EventTurnCompleted, n.Turn.Status
		}
	case notifyItemStarted, notifyItemCompleted:
		var n ItemNotification
		var item ThreadItem
		if err := json.Unmarshal(params, &n); err != nil || json.Unmarshal(n.Item, &item) != nil {
			s.log.Warn("undecodable item notification", "method", method)
			return
		}
		threadID = n.ThreadID
		ev = harness.Event{Kind: harness.EventItemStarted, TurnID: n.TurnID, ItemID: item.ID, ItemType: item.Type, Raw: n.Item}
		if method == notifyItemCompleted {
			ev.Kind = harness.EventItemCompleted
		}
		if item.Type == harness.ItemAgentMessage {
			ev.Text = item.Text
		}
		ev.Tool = toolUse(item.Type, n.Item, method == notifyItemCompleted)
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.threads[threadID]; t != nil {
		t.push(ev)
		return
	}
	if q := s.orphans[threadID]; len(q) < maxOrphans {
		s.orphans[threadID] = append(q, ev)
	}
}

// thread implements harness.Thread. Its events are queued without bound,
// so a slow consumer never blocks the connection.
type thread struct {
	s      *Session
	id     string
	onTool harness.ToolHandler

	mu            sync.Mutex
	queue         []harness.Event
	wake          chan struct{}
	ended         bool
	events        chan harness.Event
	permissions   harness.PermissionSettings
	networkPolicy map[string]any
	defaults      harness.ModelSelection
	rolloutPath   string
	fork          bool
	released      bool
	releaseDone   chan struct{}
}

func newThread(s *Session, id string, onTool harness.ToolHandler) *thread {
	t := &thread{s: s, id: id, onTool: onTool, wake: make(chan struct{}, 1), events: make(chan harness.Event), releaseDone: make(chan struct{})}
	go t.pump()
	return t
}

func (t *thread) push(ev harness.Event) {
	t.mu.Lock()
	if t.released {
		t.mu.Unlock()
		return
	}
	t.queue = append(t.queue, ev)
	t.mu.Unlock()
	t.signal()
}

func (t *thread) close() {
	t.mu.Lock()
	t.ended = true
	t.mu.Unlock()
	t.signal()
}

func (t *thread) signal() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *thread) pump() {
	defer close(t.events)
	for {
		t.mu.Lock()
		if len(t.queue) == 0 {
			ended := t.ended
			t.mu.Unlock()
			if ended {
				return
			}
			select {
			case <-t.wake:
			case <-t.releaseDone:
				return
			case <-t.s.stopped:
				return
			}
			continue
		}
		ev := t.queue[0]
		t.queue = t.queue[1:]
		t.mu.Unlock()
		select {
		case t.events <- ev:
		case <-t.releaseDone:
			return
		case <-t.s.stopped:
			// The owner closed the session and may no longer read.
			return
		}
	}
}

func (t *thread) ID() string                   { return t.id }
func (t *thread) Events() <-chan harness.Event { return t.events }

// InjectDeveloper implements harness.Thread.
func (t *thread) InjectDeveloper(ctx context.Context, texts ...string) error {
	if t.toolsReleased() {
		return fmt.Errorf("%w: disposable thread released", harness.ErrRejected)
	}
	items := make([]json.RawMessage, 0, len(texts))
	for _, text := range texts {
		b, err := json.Marshal(map[string]any{
			"type": "message", "role": developerMessageRole,
			"content": []any{map[string]any{"type": developerContentInput, "text": text}},
		})
		if err != nil {
			return fmt.Errorf("traex: encode item: %w", err)
		}
		items = append(items, b)
	}
	return t.s.conn.call(ctx, methodThreadInject, ThreadInjectItemsParams{ThreadID: t.id, Items: items}, nil)
}

// StartTurn implements harness.Thread.
func (t *thread) StartTurn(ctx context.Context, in harness.TurnInput) (string, error) {
	if t.toolsReleased() {
		return "", fmt.Errorf("%w: disposable thread released", harness.ErrRejected)
	}
	inputs := []UserInput{{Type: userInputText, Text: in.Text, TextElements: []any{}}}
	for _, image := range in.ImageURLs {
		inputs = append(inputs, UserInput{Type: "image", imageURL: image})
	}
	var res TurnStartResponse
	err := t.s.conn.call(ctx, methodTurnStart, TurnStartParams{
		ThreadID: t.id, UserMessageID: in.UserMessageID,
		Model: in.Model, ModelProvider: in.ModelProvider, Effort: in.Effort,
		Input: inputs, SandboxPolicy: t.turnNetworkPolicy(),
	}, &res)
	if err != nil {
		return "", err
	}
	if res.Turn.ID == "" {
		return "", errors.New("traex: turn/start returned no turn")
	}
	return res.Turn.ID, nil
}

// Interrupt implements harness.Thread.
func (t *thread) Interrupt(ctx context.Context, turnID string) error {
	return t.s.conn.call(ctx, methodTurnInterrupt, TurnInterruptParams{ThreadID: t.id, TurnID: turnID}, nil)
}
