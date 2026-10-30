// Package coordination transports a coordinator's native lifecycle without
// importing ledger, application, or tool-service implementations.
package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Transport persists operations before sending them to the leased runner.
type Transport interface {
	Call(context.Context, string, any, any) error
	Events(context.Context, int64) ([]Event, error)
}

type EventAcknowledger interface {
	Acknowledge(context.Context, Event) error
}

type Event struct {
	Historical   bool              `json:"-"`
	SourceRunID  string            `json:"-"`
	Sequence     int64             `json:"sequence"`
	Disconnected bool              `json:"disconnected,omitempty"`
	ThreadID     string            `json:"thread_id"`
	Native       *harness.Event    `json:"native,omitempty"`
	Call         *harness.ToolCall `json:"call,omitempty"`
}

type RetainedToolResults interface {
	ReplayToolResult(context.Context, Event) (harness.ToolResult, error)
}

// Request is a typed native operation. Tool handlers remain on the server.
type Request struct {
	ThreadID   string               `json:"thread_id,omitempty"`
	TurnID     string               `json:"turn_id,omitempty"`
	MessageID  string               `json:"message_id,omitempty"`
	Config     harness.ThreadConfig `json:"config"`
	Input      harness.TurnInput    `json:"input"`
	Texts      []string             `json:"texts,omitempty"`
	CallID     string               `json:"call_id,omitempty"`
	ToolResult harness.ToolResult   `json:"tool_result"`
}
type ThreadInfo struct {
	ID          string
	Model       harness.ModelSelection
	Permissions harness.PermissionSettings
}
type FindResult struct {
	Receipt harness.TurnReceipt
	Found   bool
}

// Session is a server-side harness whose process lives exclusively on a runner.
type Session struct {
	transport Transport
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	threads   map[string]*thread
	orphans   map[string][]Event
	once      sync.Once
	handlers  sync.WaitGroup
	cursor    int64
}

func New(ctx context.Context, t Transport) *Session {
	return NewAt(ctx, t, 0)
}

// NewAt starts a new attachment after already persisted transport events.
func NewAt(ctx context.Context, t Transport, cursor int64) *Session {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{transport: t, ctx: ctx, cancel: cancel, done: make(chan struct{}), threads: map[string]*thread{}, orphans: map[string][]Event{}, cursor: cursor}
	go s.pump()
	return s
}
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Close() error {
	var err error
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
		defer cancel()
		err = s.transport.Call(ctx, "close", Request{}, nil)
		s.cancel()
		<-s.done
	})
	return err
}
func (s *Session) Models(ctx context.Context) ([]harness.Model, error) {
	var models []harness.Model
	err := s.transport.Call(ctx, "models", Request{}, &models)
	return models, err
}
func (s *Session) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	return s.open(ctx, "start", "", cfg)
}
func (s *Session) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	return s.open(ctx, "resume", id, cfg)
}
func (s *Session) open(ctx context.Context, method, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	// Native working directories belong to the runner, never the server.
	cfg.Cwd = ""
	var info ThreadInfo
	if err := s.transport.Call(ctx, method, Request{ThreadID: id, Config: cfg}, &info); err != nil {
		return nil, err
	}
	if info.ID == "" {
		return nil, errors.New("coordination: native thread identity is missing")
	}
	t := &thread{session: s, info: info, handler: cfg.OnToolCall}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, s.ctx.Err()
	}
	pending := append(s.orphans[info.ID], s.orphans[""]...)
	sort.Slice(pending, func(i, j int) bool { return pending[i].Sequence < pending[j].Sequence })
	// Seed retained events before publishing the attachment. Start/resume
	// must return before a consumer can drain them, and newer events must
	// never overtake this backlog.
	t.events = make(chan harness.Event, len(pending)+256)
	for _, event := range pending {
		if event.Call != nil {
			s.startToolLocked(t, event)
		}
		if native := coordinatorNativeEvent(event); native != nil {
			t.events <- *native
		}
	}
	s.threads[info.ID] = t
	delete(s.orphans, info.ID)
	delete(s.orphans, "")
	s.mu.Unlock()
	return t, nil
}
func (s *Session) pump() {
	defer func() {
		s.cancel()
		s.mu.Lock()
		threads := make([]*thread, 0, len(s.threads))
		for _, t := range s.threads {
			threads = append(threads, t)
		}
		s.mu.Unlock()
		s.handlers.Wait()
		for _, t := range threads {
			t.mu.Lock()
			if !t.closed {
				close(t.events)
				t.closed = true
			}
			t.mu.Unlock()
		}
		close(s.done)
	}()
	cursor := s.cursor
	for s.ctx.Err() == nil {
		events, err := s.transport.Events(s.ctx, cursor)
		if err != nil {
			return
		}
		for _, e := range events {
			if e.Sequence <= cursor {
				continue
			}
			if e.Sequence != cursor+1 {
				return
			}
			cursor = e.Sequence
			s.mu.Lock()
			t := s.threads[e.ThreadID]
			if e.Disconnected && t == nil {
				for _, candidate := range s.threads {
					t = candidate
					break
				}
			}
			if t == nil {
				s.orphans[e.ThreadID] = append(s.orphans[e.ThreadID], e)
			}
			s.mu.Unlock()
			if t != nil && !s.deliver(t, e) {
				return
			}
			if e.Disconnected && !e.Historical {
				<-s.ctx.Done()
				return
			}
		}
	}
}
func (s *Session) startToolLocked(t *thread, e Event) {
	s.handlers.Add(1)
	go func() {
		defer s.handlers.Done()
		result := harness.ToolResult{Text: "Coordinator tool handler is unavailable"}
		if e.Historical {
			result = harness.ToolResult{Text: "Original callback result requires reconciliation; no tool was executed"}
			if retained, ok := s.transport.(RetainedToolResults); ok {
				if original, err := retained.ReplayToolResult(s.ctx, e); err == nil {
					result = original
				}
			}
		} else if t.handler != nil {
			result = t.handler(s.ctx, *e.Call)
		}
		err := s.transport.Call(s.ctx, "tool_result", Request{ThreadID: t.ID(), TurnID: e.Call.TurnID, CallID: e.Call.CallID, ToolResult: result}, nil)
		if err == nil || errors.Is(err, harness.ErrRejected) {
			if ack, ok := s.transport.(EventAcknowledger); ok {
				_ = ack.Acknowledge(s.ctx, e)
			}
		}
	}()
}

func coordinatorNativeEvent(e Event) *harness.Event {
	if e.Disconnected {
		e.Native = &harness.Event{Kind: harness.EventCoordinatorDisconnected}
		if e.Historical {
			e.Native.Kind = harness.EventCoordinatorHistoryEnd
		}
	}
	if e.Native == nil {
		return nil
	}
	copy := *e.Native
	copy.CoordinatorSequence = e.Sequence
	return &copy
}

func (s *Session) deliver(t *thread, e Event) bool {
	if e.Call != nil {
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			return false
		}
		s.startToolLocked(t, e)
		s.mu.Unlock()
	}
	if native := coordinatorNativeEvent(e); native != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.closed {
			return false
		}
		select {
		case t.events <- *native:
		case <-s.ctx.Done():
			return false
		}
	}
	return true
}

type thread struct {
	mu      sync.Mutex
	closed  bool
	session *Session
	info    ThreadInfo
	handler harness.ToolHandler
	events  chan harness.Event
}

func (t *thread) AcknowledgeEvent(ctx context.Context, event harness.Event) error {
	if event.CoordinatorSequence == 0 || event.Kind == harness.EventAgentMessageDelta {
		return nil
	}
	if ack, ok := t.session.transport.(EventAcknowledger); ok {
		return ack.Acknowledge(ctx, Event{Sequence: event.CoordinatorSequence, ThreadID: t.ID(), Native: &event, Disconnected: event.Kind == harness.EventCoordinatorDisconnected})
	}
	return nil
}

func (t *thread) ID() string                                     { return t.info.ID }
func (t *thread) Events() <-chan harness.Event                   { return t.events }
func (t *thread) ModelDefaults() harness.ModelSelection          { return t.info.Model }
func (t *thread) PermissionDefaults() harness.PermissionSettings { return t.info.Permissions }
func (t *thread) RolloutPath() string                            { return "" }
func (t *thread) InjectDeveloper(ctx context.Context, texts ...string) error {
	return t.session.transport.Call(ctx, "inject", Request{ThreadID: t.ID(), Texts: texts}, nil)
}
func (t *thread) StartTurn(ctx context.Context, in harness.TurnInput) (string, error) {
	var id string
	err := t.session.transport.Call(ctx, "turn", Request{ThreadID: t.ID(), Input: in}, &id)
	return id, err
}
func (t *thread) Interrupt(ctx context.Context, id string) error {
	return t.session.transport.Call(ctx, "interrupt", Request{ThreadID: t.ID(), TurnID: id}, nil)
}
func (t *thread) SteerTurn(ctx context.Context, id string, in harness.TurnInput) error {
	return t.session.transport.Call(ctx, "steer", Request{ThreadID: t.ID(), TurnID: id, Input: in}, nil)
}
func (t *thread) ContextUsage(ctx context.Context) (protocol.AgentContextUsage, error) {
	var usage protocol.AgentContextUsage
	err := t.session.transport.Call(ctx, "usage", Request{ThreadID: t.ID()}, &usage)
	return usage, err
}
func (t *thread) Compact(ctx context.Context) error {
	return t.session.transport.Call(ctx, "compact", Request{ThreadID: t.ID()}, nil)
}
func (t *thread) FindTurn(ctx context.Context, message, id string) (harness.TurnReceipt, bool, error) {
	var result FindResult
	err := t.session.transport.Call(ctx, "find", Request{ThreadID: t.ID(), MessageID: message, TurnID: id}, &result)
	return result.Receipt, result.Found, err
}

// Driver runs operations against the runner-local native process. The caller
// journals request admission/results and serializes lifecycle operations.
type Driver struct {
	Session      harness.Session
	Directory    string
	Instructions string
	Profile      protocol.ExecutionProfile
	Emit         func(context.Context, Event) error
	Download     func(context.Context, protocol.ArtifactFile) (string, error)
	mu           sync.Mutex
	threads      map[string]harness.Thread
	tools        map[string]chan harness.ToolResult
	wg           sync.WaitGroup
	closing      bool
}

func (d *Driver) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var r Request
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	switch method {
	case "models":
		if catalog, ok := d.Session.(harness.ModelCatalog); ok {
			return catalog.Models(ctx)
		}
		return nil, fmt.Errorf("%w: model catalog is unavailable", harness.ErrRejected)
	case "start", "resume":
		cfg := r.Config
		if cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never" {
			return nil, fmt.Errorf("%w: manager requires read-only/never", harness.ErrRejected)
		}
		if cfg.Config == nil {
			cfg.Config = map[string]any{}
		}
		for key, value := range Isolation() {
			cfg.Config[key] = value
		}
		cfg.Cwd = d.Directory
		cfg.Model = d.Profile.Model
		// Definitions can name a catalog ID while the CLI reports its native
		// model name. Resolve that alias against this executor's own catalog.
		if catalog, ok := d.Session.(harness.ModelCatalog); ok {
			models, err := catalog.Models(ctx)
			if err != nil {
				return nil, err
			}
			for _, model := range models {
				if model.ID == d.Profile.Model || model.Name == d.Profile.Model {
					cfg.Model = model.Name
					if model.Provider != "" {
						cfg.Config["model_provider"] = model.Provider
					}
					break
				}
			}
		}
		if d.Profile.Effort != "" {
			cfg.Config["model_reasoning_effort"] = d.Profile.Effort
		}
		cfg.DeveloperInstructions += "\n" + d.Instructions
		cfg.OnToolCall = d.tool
		var t harness.Thread
		var err error
		if method == "start" {
			t, err = d.Session.StartThread(ctx, cfg)
		} else {
			t, err = d.Session.ResumeThread(ctx, r.ThreadID, cfg)
		}
		if err != nil {
			return nil, err
		}
		info := ThreadInfo{ID: t.ID()}
		if m, ok := t.(harness.ConfiguredThread); ok {
			info.Model = m.ModelDefaults()
		}
		if p, ok := t.(harness.PermissionedThread); ok {
			info.Permissions = p.PermissionDefaults()
		}
		if info.Permissions.Sandbox != "read-only" || info.Permissions.ApprovalPolicy != "never" {
			return nil, fmt.Errorf("%w: native manager permissions differ", harness.ErrRejected)
		}
		if info.Model.Model != "" && info.Model.Model != cfg.Model {
			return nil, fmt.Errorf("%w: manager model differs from its pin", harness.ErrRejected)
		}
		d.mu.Lock()
		if d.threads == nil {
			d.threads = map[string]harness.Thread{}
		}
		_, exists := d.threads[t.ID()]
		d.threads[t.ID()] = t
		d.mu.Unlock()
		if !exists {
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case e, ok := <-t.Events():
						if !ok {
							d.mu.Lock()
							closing := d.closing
							d.mu.Unlock()
							if !closing {
								_ = d.Emit(ctx, Event{ThreadID: t.ID(), Disconnected: true})
							}
							return
						}
						if d.Emit(ctx, Event{ThreadID: t.ID(), Native: &e}) != nil {
							return
						}
					}
				}
			}()
		}
		return info, nil
	case "tool_result":
		d.mu.Lock()
		ch := d.tools[r.ThreadID+"/"+r.TurnID+"/"+r.CallID]
		d.mu.Unlock()
		if ch == nil {
			return nil, fmt.Errorf("%w: native tool callback is absent", harness.ErrRejected)
		}
		select {
		case ch <- r.ToolResult:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "close":
		d.mu.Lock()
		d.closing = true
		d.mu.Unlock()
		err := d.Session.Close()
		d.wg.Wait()
		return nil, err
	}
	d.mu.Lock()
	t := d.threads[r.ThreadID]
	d.mu.Unlock()
	if t == nil {
		return nil, fmt.Errorf("%w: native thread is not attached", harness.ErrRejected)
	}
	switch method {
	case "inject":
		return nil, t.InjectDeveloper(ctx, r.Texts...)
	case "turn":
		return t.StartTurn(ctx, r.Input)
	case "interrupt":
		return nil, t.Interrupt(ctx, r.TurnID)
	case "steer":
		if s, ok := t.(harness.TurnSteerer); ok {
			return nil, s.SteerTurn(ctx, r.TurnID, r.Input)
		}
	case "usage":
		if s, ok := t.(harness.ContextManager); ok {
			return s.ContextUsage(ctx)
		}
	case "compact":
		if s, ok := t.(harness.ContextManager); ok {
			return nil, s.Compact(ctx)
		}
	case "find":
		if s, ok := t.(harness.TurnReader); ok {
			receipt, found, err := s.FindTurn(ctx, r.MessageID, r.TurnID)
			return FindResult{Receipt: receipt, Found: found}, err
		}
	}
	return nil, fmt.Errorf("%w: manager native operation is unsupported", harness.ErrRejected)
}
func (d *Driver) tool(ctx context.Context, call harness.ToolCall) harness.ToolResult {
	key := call.ThreadID + "/" + call.TurnID + "/" + call.CallID
	ch := make(chan harness.ToolResult, 1)
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return harness.ToolResult{Text: "Coordinator native process closed before tool result"}
	}
	d.wg.Add(1)
	if d.tools == nil {
		d.tools = map[string]chan harness.ToolResult{}
	}
	d.tools[key] = ch
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.tools, key); d.mu.Unlock(); d.wg.Done() }()
	if err := d.Emit(ctx, Event{ThreadID: call.ThreadID, Call: &call}); err != nil {
		return harness.ToolResult{Text: "Coordinator connection requires reconciliation"}
	}
	select {
	case result := <-ch:
		if call.Tool == "download_file" && result.Success && d.Download != nil {
			var answer struct {
				OK     bool                  `json:"ok"`
				Result protocol.ArtifactFile `json:"result"`
			}
			if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || !answer.OK || answer.Result.FileID == "" {
				return harness.ToolResult{Text: "Invalid download metadata"}
			}
			path, err := d.Download(ctx, answer.Result)
			if err != nil {
				return harness.ToolResult{Text: "Issue file download failed integrity verification"}
			}
			raw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"file": answer.Result, "local_path": path}})
			return harness.ToolResult{Success: true, Text: string(raw)}
		}
		return result
	case <-ctx.Done():
		return harness.ToolResult{Text: "Coordinator lease ended"}
	case <-d.Session.Done():
		return harness.ToolResult{Text: "Coordinator native process closed before tool result"}
	}
}
