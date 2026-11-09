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

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Options start an app-server.
type Options struct {
	Environment          *codexenv.Binding
	EnvironmentExecution string
	environmentReference *codexenv.Reference
	// HostedCoordination restricts this process to Tend coordination and owner material presentation.
	// Worker sessions leave this false.
	HostedCoordination bool
	// KillWithParent binds the native process to its launcher's lifetime on
	// Linux, independently of native tool or permission configuration.
	KillWithParent bool
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
	// onStartProcess persists local ownership before any native request.
	onStartProcess       func(*exec.Cmd) error
	onBeforeCloseProcess func() error
	onClosedProcess      func() error
}

// Session is a running app-server. It implements harness.Session.
type Session struct {
	environmentReference *codexenv.Reference
	lastEnvironmentCheck time.Time
	environmentFinal     bool
	environmentAttached  bool
	toolCallbacks        int
	codex                bool // Confirmed Codex CLI; forked CLIs also return codexHome.
	conn                 *conn
	log                  *slog.Logger
	stop                 func() error

	mu      sync.Mutex
	threads map[string]*thread
	// threadLifecycle serializes Resume and Release without blocking event delivery.
	threadLifecycle chan struct{}
	// orphans holds events of threads not registered yet, such as a new
	// thread whose start response is still on its way to the caller.
	parents map[string]string
	orphans map[string][]harness.Event
	ended   bool
	// stopped is closed by Close, releasing event pumps whose consumer
	// went away.
	stopped        chan struct{}
	closed         sync.Once
	closeErr       error
	ownedProcess   bool
	ownedCloseOK   bool
	hostedBoundary *hostedToolBoundary
}

// maxOrphans bounds the events kept for threads not registered yet.
const maxOrphans = 1000

var _ harness.Session = (*Session)(nil)

// Start launches `traex app-server` and completes the handshake.
func Start(ctx context.Context, opts Options) (opened *Session, resultErr error) {
	defer func() {
		if opts.environmentReference != nil {
			state := "initialized"
			if resultErr != nil {
				state = "startup_failed"
			}
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			err := opts.environmentReference.Record(recordCtx, state)
			if err != nil && resultErr == nil {
				if opened != nil {
					_ = opened.Close()
				}
				opened = nil
				resultErr = err
			}
			if resultErr == nil && opened != nil {
				opened.environmentFinal = true
			}
		}
	}()
	if opts.Environment != nil {
		ref, err := opts.Environment.Pin(ctx, opts.EnvironmentExecution)
		if err != nil {
			return nil, fmt.Errorf("%w: environment preflight: %w", harness.ErrWorkerNotStarted, err)
		}
		opts.environmentReference = ref
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		versionCommand := exec.CommandContext(versionCtx, opts.Bin, "--version")
		procx.Contain(versionCommand)
		version, err := versionCommand.Output()
		procx.KillGroup(versionCommand)
		cancel()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(version)), "codex-cli ") {
			return nil, fmt.Errorf("%w: strict environment requires the selected native Codex executable", harness.ErrWorkerNotStarted)
		}
	}
	if opts.HostedCoordination {
		return startHostedCoordinator(ctx, opts)
	}
	return startProcess(ctx, opts, nil)
}

func startProcess(ctx context.Context, opts Options, overrides []string) (*Session, error) {
	bin := opts.Bin
	if bin == "" {
		bin = "traex"
	}
	procCtx, kill := context.WithCancel(context.Background())
	args := append(append([]string{}, overrides...), "app-server")
	cmd := exec.CommandContext(procCtx, bin, args...)
	cmd.Dir = opts.Dir
	cmd.Env = appServerEnv(os.Environ(), opts.Env)
	cleanup := func() {}
	if opts.environmentReference != nil {
		var err error
		cmd, cleanup, err = opts.environmentReference.CommandWithLifetime(ctx, procCtx, bin, args, opts.Dir, cmd.Env)
		if err != nil {
			kill()
			return nil, fmt.Errorf("%w: environment launch view: %w", harness.ErrWorkerNotStarted, err)
		}
	}
	started := false
	defer func() {
		if !started {
			cleanup()
		}
	}()
	cmd.Stderr = opts.Stderr
	procx.Contain(cmd)
	if opts.HostedCoordination || opts.KillWithParent {
		procx.DieWithParent(cmd)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		kill()
		return nil, fmt.Errorf("%w: traex: stdin: %w", harness.ErrWorkerNotStarted, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		kill()
		return nil, fmt.Errorf("%w: traex: stdout: %w", harness.ErrWorkerNotStarted, err)
	}
	if err := cmd.Start(); err != nil {
		kill()
		return nil, fmt.Errorf("%w: traex: start app-server: %w", harness.ErrWorkerNotStarted, err)
	}
	if opts.onStartProcess != nil {
		if err := opts.onStartProcess(cmd); err != nil {
			kill()
			_ = cmd.Wait()
			procx.KillGroup(cmd)
			return nil, fmt.Errorf("traex: persist app-server ownership: %w", err)
		}
	}
	s := newSession(stdout, stdin, opts.Log)
	s.environmentReference = opts.environmentReference
	s.stop = func() error {
		defer cleanup()
		var ownershipErr error
		if opts.onBeforeCloseProcess != nil {
			ownershipErr = opts.onBeforeCloseProcess()
		}
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
		if (ownershipErr == nil || errors.Is(ownershipErr, errDetachedNativeActive)) && opts.onClosedProcess != nil {
			// The original group has exited even when an observed detached
			// child remains alive. Save that group fact; StopOwnedWorker still
			// requires every recorded detached identity to have stopped.
			ownershipErr = errors.Join(ownershipErr, opts.onClosedProcess())
		}
		if opts.environmentReference != nil {
			state := "retained"
			if ownershipErr != nil {
				state = "unknown"
			} else {
				s.mu.Lock()
				unattached := s.environmentFinal && !s.environmentAttached
				s.mu.Unlock()
				if unattached {
					state = "startup_failed"
				}
			}
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			ownershipErr = errors.Join(ownershipErr, opts.environmentReference.Record(recordCtx, state))
			cancel()
		}
		return errors.Join(err, ownershipErr)
	}
	s.ownedProcess = true
	started = true
	if opts.environmentReference != nil {
		if err := opts.environmentReference.Record(ctx, "running"); err != nil {
			return nil, errors.Join(err, s.Close())
		}
	}
	if err := s.initialize(ctx, opts.Version); err != nil {
		_ = s.Close()
		if opts.environmentReference != nil {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = opts.environmentReference.Record(recordCtx, "startup_failed")
			cancel()
		}
		return nil, err
	}
	if s.codex {
		// TraeX also reports codexHome. Confirm the configured CLI itself before
		// using Codex-specific input identity and empty-input continuation.
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		versionCmd := exec.CommandContext(versionCtx, bin, "--version")
		versionCmd.Dir, versionCmd.Env = opts.Dir, cmd.Env
		procx.Contain(versionCmd)
		output, err := versionCmd.Output()
		procx.KillGroup(versionCmd)
		cancel()
		s.codex = err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "codex-cli ")
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
	s := &Session{log: log, threads: map[string]*thread{}, orphans: map[string][]harness.Event{}, parents: map[string]string{}, stopped: make(chan struct{}), threadLifecycle: make(chan struct{}, 1)}
	s.conn = newConn(r, w, handlers{request: s.serverRequest, notify: s.notification, prepare: s.prepareServerRequest}, log)
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
	var initialized struct {
		CodexHome string `json:"codexHome"`
	}
	err := s.conn.call(ctx, methodInitialize, InitializeParams{
		ClientInfo:   ClientInfo{Name: "tend", Version: version},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: true},
	}, &initialized)
	if err != nil {
		return err
	}
	if err := s.environmentReference.VerifyHome(initialized.CodexHome); err != nil {
		return err
	}
	s.codex = initialized.CodexHome != ""
	return s.conn.notify(ctx, methodInitialized, nil)
}

// Done implements harness.Session.
func (s *Session) Done() <-chan struct{} { return s.conn.done }

// Close implements harness.Session.
func (s *Session) Close() error {
	s.closed.Do(func() {
		close(s.stopped)
		s.closeErr = s.stop()
		s.mu.Lock()
		s.ownedCloseOK = s.ownedProcess && s.closeErr == nil
		s.mu.Unlock()
	})
	return s.closeErr
}

// ownedSessionCloseSucceeded applies only to the local process created by
// startProcess: its Wait returned successfully and the existing group kill was
// invoked. It does not claim all descendants or external native effects ended.
func (s *Session) ownedSessionCloseSucceeded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownedCloseOK
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
	if err := s.checkHostedConfig(ctx, cfg); err != nil {
		return nil, err
	}
	var res ThreadStartResponse
	err := s.conn.call(ctx, methodThreadStart, ThreadStartParams{
		Environments:          threadEnvironments(cfg),
		ExperimentalRawEvents: cfg.LegacyRawUsage,
		Ephemeral:             cfg.Ephemeral,
		Model:                 cfg.Model, Cwd: cfg.Cwd, ApprovalPolicy: cfg.ApprovalPolicy, ApprovalsReviewer: cfg.ApprovalsReviewer, Sandbox: cfg.Sandbox,
		DeveloperInstructions: cfg.DeveloperInstructions, Config: cfg.Config, DynamicTools: dynamicTools(cfg.Tools),
	}, &res)
	if err != nil {
		return nil, err
	}
	if err := checkEnvironment(cfg, res); err != nil {
		return nil, err
	}
	t := s.register(res.Thread.ID, cfg.OnToolCall)
	if cfg.Ephemeral || cfg.Purpose == "internal_summary" {
		t.mu.Lock()
		t.fork = true
		t.mu.Unlock()
	}
	t.setModelDefaults(res)
	t.setPermissionDefaults(res)
	t.setNetworkPolicy(cfg, res)
	t.setRolloutPath(res.Thread.Path)
	t.mu.Lock()
	t.nativeSessionID = res.Thread.SessionID
	t.mu.Unlock()
	if err := s.environmentThreadAttached(ctx, res.Thread.ID); err != nil {
		return nil, err
	}
	return t, nil
}

// ResumeThread implements harness.Session.
func (s *Session) ResumeThread(ctx context.Context, threadID string, cfg harness.ThreadConfig) (harness.Thread, error) {
	if err := s.checkHostedConfig(ctx, cfg); err != nil {
		return nil, err
	}
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
	if err := checkEnvironment(cfg, res); err != nil {
		return nil, err
	}
	t.setToolHandler(cfg.OnToolCall)
	t.setModelDefaults(res)
	t.setPermissionDefaults(res)
	t.setNetworkPolicy(cfg, res)
	t.setRolloutPath(res.Thread.Path)
	t.mu.Lock()
	t.nativeSessionID = res.Thread.SessionID
	if cfg.Purpose == "internal_summary" {
		t.fork = true
	}
	t.mu.Unlock()
	if err := s.environmentThreadAttached(ctx, res.Thread.ID); err != nil {
		return nil, err
	}
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
	return s.prepareServerRequest(method, params)(ctx)
}

func (s *Session) prepareServerRequest(method string, params json.RawMessage) func(context.Context) (any, error) {
	if method != methodToolCall {
		// Unexpected interactive requests are never approved by Tend.
		return func(context.Context) (any, error) { return nil, fmt.Errorf("%w: %s", errUnknownMethod, method) }
	}
	var p DynamicToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return func(context.Context) (any, error) { return nil, fmt.Errorf("traex: decode tool call: %w", err) }
	}
	t := s.thread(p.ThreadID)
	var handler harness.ToolHandler
	if t != nil {
		handler = t.toolHandler()
	}
	if handler == nil {
		return func(context.Context) (any, error) {
			return toolResponse(harness.ToolResult{Text: "this thread has no tools"}), nil
		}
	}
	call := harness.ToolCall{ThreadID: p.ThreadID, TurnID: p.TurnID, CallID: p.CallID, Tool: p.Tool, Arguments: p.Arguments}
	t.mu.Lock()
	if t.userTurnID == p.TurnID {
		call.UserText, call.UserItemID = t.userText, t.userItemID
	}
	t.mu.Unlock()
	if p.Namespace != nil {
		call.Namespace = *p.Namespace
	}
	s.mu.Lock()
	s.toolCallbacks++
	s.mu.Unlock()
	return func(ctx context.Context) (any, error) {
		defer func() { s.mu.Lock(); s.toolCallbacks--; s.mu.Unlock() }()
		return toolResponse(handler(ctx, call)), nil
	}
}

func toolResponse(r harness.ToolResult) DynamicToolCallResponse {
	return DynamicToolCallResponse{Success: r.Success, ContentItems: []ToolResponseItem{{Type: toolContentInputText, Text: r.Text}}}
}

func (s *Session) notification(method string, params json.RawMessage) {
	var ev harness.Event
	var threadID string
	switch method {
	case "thread/started":
		var n struct {
			Thread struct {
				ID     string `json:"id"`
				Source struct {
					SubAgent struct {
						Spawn struct {
							Parent string `json:"parent_thread_id"`
						} `json:"thread_spawn"`
					} `json:"subAgent"`
				} `json:"source"`
			} `json:"thread"`
		}
		if json.Unmarshal(params, &n) != nil || n.Thread.ID == "" || n.Thread.Source.SubAgent.Spawn.Parent == "" || n.Thread.ID == n.Thread.Source.SubAgent.Spawn.Parent {
			return
		}
		s.mu.Lock()
		s.parents[n.Thread.ID] = n.Thread.Source.SubAgent.Spawn.Parent
		pending := s.orphans[n.Thread.ID]
		delete(s.orphans, n.Thread.ID)
		s.mu.Unlock()
		for _, event := range pending {
			s.enqueueNative(n.Thread.ID, event)
		}
		return
	case "rawResponse/completed":
		var n RawResponseCompletedNotification
		if json.Unmarshal(params, &n) != nil || n.ResponseID == "" || n.ThreadID == "" {
			return
		}
		sample := protocol.TokenUsageSample{Source: "traex/response/" + n.ThreadID, RequestID: n.ResponseID, ThreadID: n.ThreadID, TurnID: n.TurnID, Revision: 1, Settlement: "final", Profile: "traex-responses-0.211.1"}
		if n.Usage != nil {
			sample.Input = n.Usage.Input
			// Native normalization may default absent cache details to zero.
			// Require the original upstream Responses detail to establish presence.
			sample.CacheRead = nil
			sample.CacheWrite = nil
			sample.Output = n.Usage.Output
			sample.Total = n.Usage.Total
		}
		if n.UsageMetadata != nil && n.Usage != nil {
			var original struct {
				InputDetails *struct {
					Cached *int64 `json:"cached_tokens"`
					Write  *int64 `json:"cache_write_tokens"`
				} `json:"input_tokens_details"`
			}
			if json.Unmarshal(n.UsageMetadata.Metadata, &original) == nil && original.InputDetails != nil {
				sample.CacheRead = original.InputDetails.Cached
				sample.CacheWrite = original.InputDetails.Write
			}
		}
		// This notification does not provide a native occurrence instant or a
		// guaranteed actual model. Neither is inferred from arrival/config.
		threadID = n.ThreadID
		ev = harness.Event{Kind: harness.EventRequestUsage, TurnID: n.TurnID, RequestUsage: &sample}
	case "error":
		var n struct {
			ThreadID  string          `json:"threadId"`
			TurnID    string          `json:"turnId"`
			Error     json.RawMessage `json:"error"`
			WillRetry bool            `json:"willRetry"`
		}
		if json.Unmarshal(params, &n) != nil || n.ThreadID == "" || n.TurnID == "" {
			return
		}
		threadID = n.ThreadID
		ev = harness.Event{Kind: harness.EventTurnError, TurnID: n.TurnID, Failure: nativeFailure(n.Error, n.WillRetry), Raw: params}
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
			ev.Failure = nativeFailure(n.Turn.Error, false)
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
		if item.Type == harness.ItemUserMessage {
			var user struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(n.Item, &user) == nil {
				for _, part := range user.Content {
					ev.Text += part.Text
				}
			}
		}
		ev.Tool = toolUse(item.Type, n.Item, method == notifyItemCompleted)
	default:
		return
	}
	s.enqueueNative(threadID, ev)
}

func (s *Session) enqueueNative(threadID string, ev harness.Event) {

	s.mu.Lock()
	defer s.mu.Unlock()
	if parent := s.parents[threadID]; parent != "" {
		original := threadID
		seen := map[string]bool{threadID: true}
		for parent != "" && !seen[parent] {
			seen[parent] = true
			threadID = parent
			parent = s.parents[parent]
		}
		if ev.RequestUsage != nil {
			// A buffered descendant may already carry its direct native parent.
			// Learning another ancestor changes routing, not that identity.
			if ev.RequestUsage.ParentThreadID == "" {
				ev.RequestUsage.ParentThreadID = s.parents[original]
			}
		} else if ev.Kind == harness.EventChildRollout {
			// Preserve the original child envelope when forwarding it again.
		} else if ev.ContextUsage == nil && ev.Kind != harness.EventAgentMessageDelta {
			raw, _ := json.Marshal(struct {
				Thread string            `json:"thread_id"`
				Turn   string            `json:"turn_id"`
				Kind   harness.EventKind `json:"kind"`
				Data   json.RawMessage   `json:"data,omitempty"`
			}{original, ev.TurnID, ev.Kind, ev.Raw})
			ev = harness.Event{Kind: harness.EventChildRollout, TurnID: ev.TurnID, Raw: raw}
		} else {
			return
		}
	}
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

	mu                               sync.Mutex
	queue                            []harness.Event
	wake                             chan struct{}
	ended                            bool
	started                          bool
	events                           chan harness.Event
	permissions                      harness.PermissionSettings
	networkPolicy                    map[string]any
	defaults                         harness.ModelSelection
	nativeSessionID                  string
	rolloutPath                      string
	fork                             bool
	released                         bool
	releaseDone                      chan struct{}
	userText, userItemID, userTurnID string
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
	if ev.Kind == harness.EventTurnStarted {
		t.started = true
	}
	if ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemUserMessage {
		t.userText, t.userItemID, t.userTurnID = ev.Text, ev.ItemID, ev.TurnID
	}
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
	if err := t.s.checkHostedBoundary(ctx); err != nil {
		// No turn/start RPC has been sent. Keep the transport cause without
		// turning this local admission failure into an unknown native start.
		return "", fmt.Errorf("%w: hosted pre-start check: %w", harness.ErrRejected, err)
	}
	if t.s.hostedBoundary != nil {
		permissions := t.PermissionDefaults()
		if permissions.Sandbox != "read-only" || permissions.ApprovalPolicy != "never" {
			return "", fmt.Errorf("%w: hosted native permissions differ from the required read-only boundary", harness.ErrRejected)
		}
	}
	if t.toolsReleased() {
		return "", fmt.Errorf("%w: disposable thread released", harness.ErrRejected)
	}
	inputs := []UserInput{}
	if !in.Continue {
		inputs = append(inputs, UserInput{Type: userInputText, Text: in.Text, TextElements: []any{}})
	}
	if !in.Continue {
		for _, image := range in.ImageURLs {
			inputs = append(inputs, UserInput{Type: "image", imageURL: image})
		}
	}
	var res TurnStartResponse
	params := TurnStartParams{ThreadID: t.id, UserMessageID: in.UserMessageID, Model: in.Model, ModelProvider: in.ModelProvider, Effort: in.Effort, Input: inputs, SandboxPolicy: t.turnNetworkPolicy()}
	var wire any = params
	if t.s.codex {
		// Codex 0.163 uses clientUserMessageId; TraeX retains userMessageId.
		data, _ := json.Marshal(params)
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			return "", err
		}
		delete(fields, "userMessageId")
		if in.UserMessageID != "" && !in.Continue {
			fields["clientUserMessageId"] = in.UserMessageID
		}
		wire = fields
	}
	t.mu.Lock()
	t.started = true
	t.mu.Unlock()
	err := t.s.conn.call(ctx, methodTurnStart, wire, &res)
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

// Steer never starts, interrupts or resumes a turn. A mismatched receipt leaves
// the result unknown even when the transport successfully returned a response.
func (t *thread) Steer(ctx context.Context, turnID, text string) error {
	return t.SteerTurn(ctx, turnID, harness.TurnInput{Text: text})
}

var errSteerIdentity = errors.New("traex: steer receipt turn does not match")

func threadEnvironments(cfg harness.ThreadConfig) []ThreadEnvironment {
	if cfg.EnvironmentID == "" {
		return nil
	}
	return []ThreadEnvironment{{EnvironmentID: cfg.EnvironmentID, Cwd: cfg.Cwd}}
}
func checkEnvironment(cfg harness.ThreadConfig, res ThreadStartResponse) error {
	if cfg.EnvironmentID == "" {
		return nil
	}
	if len(res.Thread.Environments) != 1 || res.Thread.Environments[0].EnvironmentID != cfg.EnvironmentID || res.Thread.Environments[0].Cwd != cfg.Cwd {
		return fmt.Errorf("%w: remote execution environment differs", harness.ErrRejected)
	}
	return nil
}
