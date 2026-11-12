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
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Transport persists operations before sending them to the leased runner.
// Once its event stream ends, resume may only expose confirmed local closing
// metadata. It must never dispatch new native work on that ended connection.
type Transport interface {
	Call(context.Context, string, any, any) error
	Events(context.Context, int64) ([]Event, error)
}

type EventAcknowledger interface {
	Acknowledge(context.Context, Event) error
}

type ReplyPrefixAcknowledger interface {
	AcknowledgeReplyPrefix(context.Context, Event, []int64) error
}

type Event struct {
	Historical     bool              `json:"-"`
	SourceRunID    string            `json:"-"`
	SourceSequence int64             `json:"-"`
	Sequence       int64             `json:"sequence"`
	Disconnected   bool              `json:"disconnected,omitempty"`
	ThreadID       string            `json:"thread_id"`
	Native         *harness.Event    `json:"native,omitempty"`
	Call           *harness.ToolCall `json:"call,omitempty"`
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
	ended     bool
	closing   bool
	once      sync.Once
	closeErr  error
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
	s.once.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
		defer cancel()
		s.closeErr = s.transport.Call(ctx, "close", Request{}, nil)
		s.cancel()
		<-s.done
	})
	return s.closeErr
}

// call preserves each caller's deadline while also ending pending operations
// when their connection ends. Cancellation never establishes a native outcome.
func (s *Session) call(ctx context.Context, method string, input, output any) (callErr error) {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return fmt.Errorf("%w: coordinator connection is closing", harness.ErrRejected)
	}
	caller := ctx
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() {
		stop()
		cancel()
		if callErr == nil || caller.Err() != nil || s.ctx.Err() == nil {
			return
		}
		switch method {
		case "models", "find", "usage":
			// These calls have no native business effect. Connection EOF may
			// beat a ledger read of physical end; preserve the same rejection
			// in either order, without reclassifying a lost mutation reply.
			callErr = fmt.Errorf("%w: native observation ended with its connection: %v", harness.ErrRejected, callErr)
		}
	}()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.transport.Call(ctx, method, input, output)
}

func (s *Session) Models(ctx context.Context) ([]harness.Model, error) {
	var models []harness.Model
	err := s.call(ctx, "models", Request{}, &models)
	return models, err
}
func (s *Session) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	return s.open(ctx, "start", "", cfg)
}
func (s *Session) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	return s.open(ctx, "resume", id, cfg)
}
func (s *Session) ForkThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	if cfg.Purpose != "internal_summary" || cfg.Ephemeral {
		return nil, fmt.Errorf("%w: only persistent private summaries may fork", harness.ErrRejected)
	}
	return s.open(ctx, "fork", id, cfg)
}
func (s *Session) ReleaseThread(ctx context.Context, id string) error {
	return s.call(ctx, "release", Request{ThreadID: id}, nil)
}
func (s *Session) open(ctx context.Context, method, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	// Native working directories belong to the runner, never the server.
	cfg.Cwd = ""
	request := Request{ThreadID: id, Config: cfg}
	var info ThreadInfo
	s.mu.Lock()
	ended, closing := s.ended, s.closing
	s.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("%w: coordinator connection is closing", harness.ErrRejected)
	}
	var err error
	attachRetained := ended
	if !ended {
		err = s.call(ctx, method, request, &info)
		s.mu.Lock()
		ended, closing = s.ended, s.closing
		s.mu.Unlock()
		attachRetained = ended && err != nil
	}
	if attachRetained && !closing && method == "resume" && ctx.Err() == nil {
		// The transport has ended, but confirmed closing metadata and original
		// tails remain readable. Transport.Call must only return local metadata
		// here; its ended stream cannot dispatch another native operation.
		err = s.transport.Call(ctx, method, request, &info)
	} else if attachRetained && err == nil {
		err = fmt.Errorf("%w: coordinator connection ended", harness.ErrRejected)
	}
	if err != nil {
		return nil, err
	}
	if info.ID == "" {
		return nil, errors.New("coordination: native thread identity is missing")
	}
	t := &thread{session: s, info: info, handler: cfg.OnToolCall}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, fmt.Errorf("%w: coordinator connection is closing", harness.ErrRejected)
	}
	if s.ctx.Err() != nil && !s.ended {
		return nil, s.ctx.Err()
	}
	if s.ended {
		if current := s.threads[info.ID]; current != nil {
			return current, nil
		}
	}
	pending := append(s.orphans[info.ID], s.orphans[""]...)
	sort.Slice(pending, func(i, j int) bool { return pending[i].Sequence < pending[j].Sequence })
	// Seed retained events before publishing the attachment. This is also the
	// local drain-only path after a pre-attachment connection end.
	t.events = make(chan harness.Event, len(pending)+256)
	for _, event := range pending {
		if event.Call != nil && !s.ended {
			s.startToolLocked(t, event)
		}
		if native := coordinatorNativeEvent(event); native != nil {
			t.events <- *native
		}
	}
	if s.ended {
		close(t.events)
		t.closed = true
	}
	s.threads[info.ID] = t
	delete(s.orphans, info.ID)
	delete(s.orphans, "")
	return t, nil
}
func (s *Session) pump() {
	defer func() {
		s.mu.Lock()
		s.ended = true
		s.mu.Unlock()
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
			var recipients []*thread
			connectionEnd := e.Disconnected && !e.Historical
			if e.Disconnected && e.ThreadID == "" {
				// A connection-level end belongs to every attached consumer,
				// including a front thread created after a toolset handoff.
				for _, t := range s.threads {
					recipients = append(recipients, t)
				}
				if len(recipients) == 0 {
					s.orphans[""] = append(s.orphans[""], e)
				}
			} else if t := s.threads[e.ThreadID]; t != nil {
				recipients = append(recipients, t)
			} else {
				s.orphans[e.ThreadID] = append(s.orphans[e.ThreadID], e)
			}
			s.mu.Unlock()
			for _, t := range recipients {
				if connectionEnd {
					s.deliverConnectionEnd(t, e)
				} else if !s.deliver(t, e) {
					return
				}
			}
			if connectionEnd {
				// No consumer must issue Close before the connection can end.
				// Deferred cancellation releases calls/handlers and closes all
				// streams after their already queued tail, without a new turn.
				return
			}
		}
	}
}

// deliverConnectionEnd cannot wait for an abandoned thread to drain. A full
// buffer keeps every earlier event and reaches EOF when drained; other attached
// consumers can acknowledge the original end marker. The durable transport
// retains any unacknowledged marker for its later history reader.
func (s *Session) deliverConnectionEnd(t *thread, e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	select {
	case t.events <- *coordinatorNativeEvent(e):
	default:
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
	copy.OriginRunID, copy.OriginSequence = e.SourceRunID, e.SourceSequence
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

// AcknowledgeReplyPrefix is separate from event acknowledgement: a persisted
// interrupted reply is not a native item-completed or turn-completed fact.
func (t *thread) AcknowledgeReplyPrefix(ctx context.Context, event harness.Event, consumed []int64) error {
	if event.CoordinatorSequence == 0 || event.Kind != harness.EventAgentMessageDelta {
		return nil
	}
	if ack, ok := t.session.transport.(ReplyPrefixAcknowledger); ok {
		return ack.AcknowledgeReplyPrefix(ctx, Event{Sequence: event.CoordinatorSequence, ThreadID: t.ID(), Native: &event}, consumed)
	}
	return nil
}

func (t *thread) ID() string                                     { return t.info.ID }
func (t *thread) Events() <-chan harness.Event                   { return t.events }
func (t *thread) ModelDefaults() harness.ModelSelection          { return t.info.Model }
func (t *thread) PermissionDefaults() harness.PermissionSettings { return t.info.Permissions }
func (t *thread) RolloutPath() string                            { return "" }
func (t *thread) InjectDeveloper(ctx context.Context, texts ...string) error {
	return t.session.call(ctx, "inject", Request{ThreadID: t.ID(), Texts: texts}, nil)
}
func (t *thread) StartTurn(ctx context.Context, in harness.TurnInput) (string, error) {
	var id string
	err := t.session.call(ctx, "turn", Request{ThreadID: t.ID(), Input: in}, &id)
	return id, err
}
func (t *thread) Interrupt(ctx context.Context, id string) error {
	return t.session.call(ctx, "interrupt", Request{ThreadID: t.ID(), TurnID: id}, nil)
}
func (t *thread) SteerTurn(ctx context.Context, id string, in harness.TurnInput) error {
	return t.session.call(ctx, "steer", Request{ThreadID: t.ID(), TurnID: id, Input: in}, nil)
}
func (t *thread) ContextUsage(ctx context.Context) (protocol.AgentContextUsage, error) {
	var usage protocol.AgentContextUsage
	err := t.session.call(ctx, "usage", Request{ThreadID: t.ID()}, &usage)
	return usage, err
}
func (t *thread) Compact(ctx context.Context) error {
	return t.session.call(ctx, "compact", Request{ThreadID: t.ID()}, nil)
}
func (t *thread) FindTurn(ctx context.Context, message, id string) (harness.TurnReceipt, bool, error) {
	var result FindResult
	err := t.session.call(ctx, "find", Request{ThreadID: t.ID(), MessageID: message, TurnID: id}, &result)
	return result.Receipt, result.Found, err
}

// Driver runs operations against the runner-local native process. The caller
// journals request admission/results and serializes lifecycle operations.
type Driver struct {
	RequireReceiptReader bool
	NativeOwnerTools     bool
	OnSource             func(context.Context, protocol.RolloutPreviewSource) error
	// LegacyRawUsage preserves the existing event path for servers without ledger capability.
	LegacyRawUsage      bool
	OnUsageSource       func(context.Context, nativeusage.Source) error
	BeforeUsageDispatch func(context.Context, nativeusage.Source) error
	OnUsageStopped      func(context.Context, string) error
	Session             harness.Session
	Directory           string
	Instructions        string
	Profile             protocol.ExecutionProfile
	Emit                func(context.Context, Event) error
	Download            func(context.Context, protocol.ArtifactFile) (string, error)
	Upload              func(context.Context, string, string) (protocol.ArtifactFile, error)
	mu                  sync.Mutex
	threads             map[string]harness.Thread
	private             map[string]bool
	tools               map[string]chan harness.ToolResult
	wg                  sync.WaitGroup
	closing             bool
	failures            chan error
	forwarding          map[string]bool
}

// Failures reports transport failures to the execution supervisor. A live
// provider process does not mean its event consumer is still functioning.
func (d *Driver) Failures() <-chan error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures == nil {
		d.failures = make(chan error, 1)
	}
	return d.failures
}

func (d *Driver) emit(ctx context.Context, event Event) error {
	err := d.Emit(ctx, event)
	if err != nil && ctx.Err() == nil {
		kind := "tool_callback"
		turn, call := "", ""
		if event.Native != nil {
			kind = string(event.Native.Kind)
			turn = event.Native.TurnID
		} else if event.Disconnected {
			kind = "disconnected"
		} else if event.Call != nil {
			turn, call = event.Call.TurnID, event.Call.CallID
		}
		failure := fmt.Errorf("coordinator event %s thread %s turn %s call %s: %w", kind, event.ThreadID, turn, call, err)
		d.Failures()
		d.mu.Lock()
		select {
		case d.failures <- failure:
		default:
		}
		d.mu.Unlock()
	}
	return err
}

func (d *Driver) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if d.RequireReceiptReader && method != "resume" && method != "find" && method != "usage" && method != "close" {
		return nil, fmt.Errorf("%w: receipt-only manager cannot execute native business operations", harness.ErrRejected)
	}
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
	case "start", "resume", "fork":
		cfg := r.Config
		receiptHistory := d.RequireReceiptReader || cfg.Purpose == harness.ReceiptHistoryPurpose
		nativeOwner := d.NativeOwnerTools && !d.RequireReceiptReader
		if !nativeOwner && (cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never") {
			return nil, fmt.Errorf("%w: receipt or legacy transport requires read-only/never", harness.ErrRejected)
		}
		if d.RequireReceiptReader && (r.ThreadID == "" || cfg.Ephemeral) {
			return nil, fmt.Errorf("%w: receipt-only history requires the original persistent thread", harness.ErrRejected)
		}
		if receiptHistory && (method != "resume" || r.ThreadID == "" || cfg.Ephemeral) {
			return nil, fmt.Errorf("%w: history reads require an original persistent thread", harness.ErrRejected)
		}
		if cfg.Config == nil {
			cfg.Config = map[string]any{}
		}
		if !nativeOwner {
			for key, value := range Isolation() {
				cfg.Config[key] = value
			}
		}
		if d.Upload != nil && nativeOwner && !receiptHistory {
			cfg.Tools = append(cfg.Tools, harness.UploadFileToolSpec)
		}
		cfg.Cwd = d.Directory
		if !receiptHistory {
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
		}
		cfg.DeveloperInstructions += "\n" + d.Instructions
		cfg.LegacyRawUsage = d.LegacyRawUsage
		cfg.OnToolCall = d.tool
		var t harness.Thread
		var err error
		if receiptHistory {
			reader, ok := d.Session.(harness.ThreadHistoryReader)
			if !ok {
				return nil, fmt.Errorf("%w: non-executing native history reader is unavailable", harness.ErrRejected)
			}
			t, err = reader.ReadThreadHistory(ctx, r.ThreadID)
		} else if method == "start" {
			t, err = d.Session.StartThread(ctx, cfg)
		} else if method == "fork" {
			d.mu.Lock()
			parent := d.threads[r.ThreadID]
			d.mu.Unlock()
			forker, ok := d.Session.(harness.ThreadForker)
			if !ok || parent == nil || cfg.Purpose != "internal_summary" || cfg.Ephemeral {
				return nil, fmt.Errorf("%w: private summary fork is unavailable", harness.ErrRejected)
			}
			t, err = forker.ForkThread(ctx, r.ThreadID, cfg)
		} else {
			t, err = d.Session.ResumeThread(ctx, r.ThreadID, cfg)
		}
		if err != nil {
			return nil, err
		}
		if d.OnUsageSource != nil {
			if native, ok := t.(harness.NativeUsageThread); ok {
				if err := d.OnUsageSource(ctx, d.privateUsageSource(native.UsageSource(), cfg, r.ThreadID)); err != nil {
					return nil, err
				}
			}
		}
		if d.OnSource != nil {
			source := protocol.RolloutPreviewSource{SessionID: t.ID()}
			if location, ok := t.(harness.ThreadLocation); ok {
				source.Path = location.RolloutPath()
			}
			if err := d.OnSource(ctx, source); err != nil {
				return nil, err
			}
		}
		info := ThreadInfo{ID: t.ID()}
		if m, ok := t.(harness.ConfiguredThread); ok {
			info.Model = m.ModelDefaults()
		}
		if p, ok := t.(harness.PermissionedThread); ok {
			info.Permissions = p.PermissionDefaults()
		}
		if !nativeOwner && !receiptHistory && (info.Permissions.Sandbox != "read-only" || info.Permissions.ApprovalPolicy != "never") {
			return nil, fmt.Errorf("%w: native manager permissions differ", harness.ErrRejected)
		}
		if info.Model.Model != "" && info.Model.Model != cfg.Model {
			return nil, fmt.Errorf("%w: manager model differs from its pin", harness.ErrRejected)
		}
		d.mu.Lock()
		if d.threads == nil {
			d.threads = map[string]harness.Thread{}
		}
		d.threads[t.ID()] = t
		if d.forwarding == nil {
			d.forwarding = map[string]bool{}
		}
		startForwarding := !receiptHistory && !d.forwarding[t.ID()]
		if startForwarding {
			d.forwarding[t.ID()] = true
		}
		if d.private == nil {
			d.private = map[string]bool{}
		}
		if cfg.Purpose == "internal_summary" {
			d.private[t.ID()] = true
		}
		d.mu.Unlock()
		if startForwarding {
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
							closing := d.closing || d.private[t.ID()]
							d.mu.Unlock()
							if !closing {
								_ = d.emit(ctx, Event{ThreadID: t.ID(), Disconnected: true})
							}
							return
						}
						if d.emit(ctx, Event{ThreadID: t.ID(), Native: &e}) != nil {
							return
						}
					}
				}
			}()
		}
		return info, nil
	case "release":
		d.mu.Lock()
		private := d.private[r.ThreadID]
		d.mu.Unlock()
		forker, ok := d.Session.(harness.ThreadForker)
		if !private || !ok {
			return nil, fmt.Errorf("%w: cannot release an owning thread", harness.ErrRejected)
		}
		if err := forker.ReleaseThread(ctx, r.ThreadID); err != nil {
			return nil, err
		}
		if d.OnUsageStopped != nil {
			return nil, d.OnUsageStopped(ctx, r.ThreadID)
		}
		return nil, nil
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
		if d.BeforeUsageDispatch != nil {
			if native, ok := t.(harness.NativeUsageThread); ok {
				if err := d.BeforeUsageDispatch(ctx, native.UsageSource()); err != nil {
					return nil, err
				}
			}
		}
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
		if d.BeforeUsageDispatch != nil {
			if native, ok := t.(harness.NativeUsageThread); ok {
				if err := d.BeforeUsageDispatch(ctx, native.UsageSource()); err != nil {
					return nil, err
				}
			}
		}
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
	if d.RequireReceiptReader {
		return harness.ToolResult{Text: "Receipt-only managers cannot execute native tool callbacks"}
	}
	if call.Tool == harness.UploadFileTool && !d.NativeOwnerTools {
		return harness.ToolResult{Text: "This receipt or legacy transport does not support native file upload"}
	}

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
	if call.Tool == harness.UploadFileTool && d.Upload != nil {
		var p struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if json.Unmarshal(call.Arguments, &p) != nil || p.Path == "" || p.Name == "" {
			return harness.ToolResult{Text: "path and name are required"}
		}
		file, err := d.Upload(ctx, p.Path, p.Name)
		if err != nil {
			return harness.ToolResult{Text: "file upload remains unconfirmed: " + err.Error() + "; preserve the original file and name to reconcile"}
		}
		raw, _ := json.Marshal(map[string]any{"ok": true, "result": file})
		return harness.ToolResult{Success: true, Text: string(raw)}
	}

	if err := d.emit(ctx, Event{ThreadID: call.ThreadID, Call: &call}); err != nil {
		if ctx.Err() != nil {
			return harness.ToolResult{Text: "Coordinator execution ended before tool event delivery; preserve the original call for reconciliation"}
		}
		return harness.ToolResult{Text: "Coordinator tool event delivery failed; the execution supervisor has been notified. Preserve the original call for reconciliation"}
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

func (d *Driver) privateUsageSource(src nativeusage.Source, cfg harness.ThreadConfig, parent string) nativeusage.Source {
	src.Purpose = cfg.Purpose
	if cfg.Purpose == "internal_summary" {
		src.ParentSessionID = parent
		d.mu.Lock()
		var origin harness.Thread
		if parent != "" {
			origin = d.threads[parent]
		} else {
			for id, thread := range d.threads {
				if !d.private[id] {
					origin = thread
					break
				}
			}
		}
		d.mu.Unlock()
		if native, ok := origin.(harness.NativeUsageThread); ok {
			source := native.UsageSource()
			src.ParentSessionID = source.SessionID
			if src.ParentSessionID == "" {
				src.ParentSessionID = source.ThreadID
			}
		}
	}
	return src
}
