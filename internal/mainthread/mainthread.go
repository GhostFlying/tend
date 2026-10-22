// Package mainthread runs the main agent's front thread (D20): it keeps a
// harness session and thread alive, hands each conversation message to the
// thread as one turn, injects context before the turn, answers the main
// agent's tool calls and records what the agent says.
//
// Messages arrive through the outbox, so the manager is the dispatcher's
// handler for the main thread destination. A delivery returns once its
// turn ended, which keeps the front thread to one turn at a time (v0.1
// §6.2). Every delivery writes its turn record before turn/start: after a
// restart, a delivery without a record certainly never started, one whose
// turn started is done, and one stuck before confirmation is unknown and
// left to the owner rather than resent.
package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

// Config configures the manager.
type Config struct {
	// Instance names the harness instance; it is part of the main agent's
	// identity and stays the same across restarts.
	Instance string
	// Thread configures the front thread; the manager adds the tools.
	Thread harness.ThreadConfig
	// Open starts a harness session.
	Open func(ctx context.Context) (harness.Session, error)
	// RestartDelay separates attempts to bring a session back. Default 2s.
	RestartDelay time.Duration
	Log          *slog.Logger
}

// Manager runs the front thread.
type Manager struct {
	app     *app.App
	context *contextbuilder.Builder
	tools   *tools.Service
	cfg     Config

	mu      sync.Mutex
	session harness.Session
	thread  harness.Thread
	ready   chan struct{} // closed while a thread is up
	active  *turn
	settled map[string]string // turn ID -> final status, for turns that ended before Deliver saw them start
}

// turn is the delivery the front thread is working on.
type turn struct {
	requestID string
	messageID string
	turnID    string
	done      chan string
}

// New returns a Manager.
func New(a *app.App, cb *contextbuilder.Builder, ts *tools.Service, cfg Config) *Manager {
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = 2 * time.Second
	}
	return &Manager{app: a, context: cb, tools: ts, cfg: cfg, ready: make(chan struct{}), settled: map[string]string{}}
}

func (m *Manager) threadKey(threadID string) string { return m.cfg.Instance + "/" + threadID }

// Run keeps a session and the front thread up until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := m.serve(ctx); err != nil && ctx.Err() == nil {
			m.cfg.Log.ErrorContext(ctx, "main thread session ended", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(m.cfg.RestartDelay):
		}
	}
	return ctx.Err()
}

// serve runs one session: opens it, resumes or starts the thread and
// handles the thread's events until the session ends.
func (m *Manager) serve(ctx context.Context) error {
	s, err := m.cfg.Open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	th, err := m.openThread(ctx, s)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.session, m.thread = s, th
	close(m.ready)
	m.mu.Unlock()
	defer func() {
		m.detach(th)
		m.mu.Lock()
		a := m.active
		m.mu.Unlock()
		if a != nil {
			select {
			case a.done <- store.TurnInterrupted:
			default:
			}
		}
		if err := m.app.SettleOpenTurns(context.WithoutCancel(ctx), th.ID()); err != nil {
			m.cfg.Log.ErrorContext(ctx, "could not settle interrupted turns", "err", err)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-th.Events():
			if !ok {
				return errors.New("mainthread: the harness ended the thread")
			}
			m.handle(ctx, th, ev)
		}
	}
}

// detach takes th out of service, once: deliveries wait for the next
// session from then on.
func (m *Manager) detach(th harness.Thread) harness.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.thread != th {
		return nil
	}
	s := m.session
	m.session, m.thread = nil, nil
	m.ready = make(chan struct{})
	return s
}

func (m *Manager) threadConfig() harness.ThreadConfig {
	cfg := m.cfg.Thread
	cfg.Tools = tools.Specs()
	cfg.OnToolCall = m.onToolCall
	return cfg
}

// openThread resumes the instance's front thread, or starts a new one
// when there is none or it cannot be resumed. A new thread has no
// baseline, so its first turn gets a full snapshot.
func (m *Manager) openThread(ctx context.Context, s harness.Session) (harness.Thread, error) {
	id, ok, err := m.app.MainThread(ctx, m.cfg.Instance)
	if err != nil {
		return nil, err
	}
	if ok {
		// Turns still running belonged to a harness that is gone, such as
		// one a crashed tend process took down with it; the harness ends
		// them as interrupted (docs/findings/phase-b-traex.md).
		if err := m.app.SettleOpenTurns(ctx, id); err != nil {
			return nil, err
		}
		th, err := s.ResumeThread(ctx, id, m.threadConfig())
		if err == nil {
			return th, nil
		}
		if !errors.Is(err, harness.ErrRejected) {
			return nil, err
		}
		m.cfg.Log.WarnContext(ctx, "front thread cannot be resumed; starting a new one", "thread", id, "err", err)
	}
	th, err := s.StartThread(ctx, m.threadConfig())
	if err != nil {
		return nil, err
	}
	return th, m.app.SetMainThread(ctx, m.cfg.Instance, th.ID())
}

// handle acts on one thread event.
func (m *Manager) handle(ctx context.Context, th harness.Thread, ev harness.Event) {
	switch {
	case ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemContextCompaction:
		// The harness replaced the history; what tend injected is gone.
		if err := m.context.Invalidate(ctx, m.threadKey(th.ID())); err != nil {
			m.cfg.Log.ErrorContext(ctx, "could not invalidate the context baseline", "err", err)
		}
	case ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemAgentMessage && ev.Text != "":
		if err := m.recordReply(ctx, th, ev); err != nil {
			m.cfg.Log.ErrorContext(ctx, "could not record the agent's message", "turn", ev.TurnID, "err", err)
		}
	case ev.Tool != nil:
		if err := m.recordToolUse(ctx, th, ev); err != nil {
			m.cfg.Log.ErrorContext(ctx, "could not record the agent's tool use", "turn", ev.TurnID, "item", ev.ItemID, "err", err)
		}
	case ev.Kind == harness.EventTurnStarted:
		// One turn at a time: the first turn to start after a turn/start
		// is that delivery's, even before the start response arrives.
		m.mu.Lock()
		if a := m.active; a != nil && a.turnID == "" {
			a.turnID = ev.TurnID
		}
		m.mu.Unlock()
	case ev.Kind == harness.EventTurnCompleted:
		m.mu.Lock()
		a := m.active
		if a != nil && a.turnID == ev.TurnID {
			select {
			case a.done <- ev.TurnStatus:
			default:
			}
		} else {
			m.settled[ev.TurnID] = ev.TurnStatus
		}
		m.mu.Unlock()
	}
}

// recordReply stores an agent message, keyed by its item so a repeated
// event records it once.
func (m *Manager) recordReply(ctx context.Context, th harness.Thread, ev harness.Event) error {
	payload := map[string]string{"body": ev.Text, "reply_to": m.replyTo(ev.TurnID), "turn_id": ev.TurnID, "item_id": ev.ItemID}
	return m.record(ctx, th, th.ID()+"/"+ev.TurnID+"/item:"+ev.ItemID, "conversation.record_agent_message", payload, app.RecordAgentMessage{})
}

// recordToolUse stores a tool use as it starts and as it ends, keyed by
// its item and the event so a repeated event records it once (D69).
func (m *Manager) recordToolUse(ctx context.Context, th harness.Thread, ev harness.Event) error {
	phase := "started"
	if ev.Kind == harness.EventItemCompleted {
		phase = "completed"
	}
	payload := map[string]any{"turn_id": ev.TurnID, "item_id": ev.ItemID, "reply_to": m.replyTo(ev.TurnID),
		"tool": ev.Tool.Name, "input": ev.Tool.Input, "output": ev.Tool.Output, "status": ev.Tool.Status}
	return m.record(ctx, th, th.ID()+"/"+ev.TurnID+"/item:"+ev.ItemID+"/"+phase, "conversation.record_tool_use", payload, app.RecordToolUse{})
}

// replyTo is the message the turn handles, when it is the active one.
func (m *Manager) replyTo(turnID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil && m.active.turnID == turnID {
		return m.active.messageID
	}
	return ""
}

// record executes a conversation write of the main agent under a request
// ID derived from native.
func (m *Manager) record(ctx context.Context, th harness.Thread, native, op string, payload any, cmd app.Command) error {
	requestID, err := m.app.NativeRequestID(ctx, "harness:"+m.cfg.Instance, native)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := &protocol.Envelope{RequestID: requestID, Operation: op, Target: protocol.Target{Type: "message"}, Payload: body}
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: m.cfg.Instance, Thread: th.ID()}
	_, err = m.app.Execute(ctx, principal, env, cmd)
	return err
}

// onToolCall answers the main agent's tool calls with the identity of the
// connection and the message the call's turn handles: the recorded
// delivery of that turn, so a replayed call keeps its original identity,
// or else the delivery whose turn is starting.
func (m *Manager) onToolCall(ctx context.Context, c harness.ToolCall) harness.ToolResult {
	call := tools.Call{
		Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: m.cfg.Instance, Thread: c.ThreadID},
		Native:    c.ThreadID + "/" + c.TurnID + "/" + c.CallID, Tool: c.Tool, Arguments: c.Arguments,
	}
	rec, ok, err := m.app.TurnDelivery(ctx, c.ThreadID, c.TurnID)
	if err != nil {
		m.cfg.Log.ErrorContext(ctx, "could not look up the call's turn", "turn", c.TurnID, "err", err)
	}
	if ok {
		call.SourceMessageID, call.ParentRequestID = rec.MessageID, rec.RequestID
	} else {
		m.mu.Lock()
		if a := m.active; a != nil && (a.turnID == "" || a.turnID == c.TurnID) {
			call.SourceMessageID, call.ParentRequestID = a.messageID, a.requestID
		}
		m.mu.Unlock()
	}
	return m.tools.Handle(ctx, call)
}

// waitThread returns the front thread once a session is up.
func (m *Manager) waitThread(ctx context.Context) (harness.Thread, error) {
	for {
		m.mu.Lock()
		th, ready := m.thread, m.ready
		m.mu.Unlock()
		if th != nil {
			return th, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ready:
		}
	}
}

// Deliver implements dispatch.Handler: it hands a message to the front
// thread as one turn and returns when the turn ended.
func (m *Manager) Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p app.HandleMessage
	if err := json.Unmarshal(d.Payload, &p); err != nil || p.MessageID == "" {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "the delivery names no message"}
	}
	rec, ok, err := m.app.MainTurn(ctx, d.RequestID)
	switch {
	case err != nil:
		return retry(err)
	case ok && rec.Status == store.TurnStarting:
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "an earlier attempt may have started the turn"}
	case ok && rec.Status != store.TurnRejected:
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	msg, err := m.app.Message(ctx, p.MessageID)
	if err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "the message does not exist"}
	}
	th, err := m.waitThread(ctx)
	if err != nil {
		return retry(err)
	}
	key := m.threadKey(th.ID())
	// Recall the conversation as the sender's client showed it (D27).
	focus := msg.Seq
	if msg.LastSeenSeq > 0 && msg.LastSeenSeq < msg.Seq {
		focus = msg.LastSeenSeq + 1
	}
	packet, err := m.context.Prepare(ctx, key, focus)
	if err != nil {
		return retry(err)
	}
	if packet != nil {
		if err := th.InjectDeveloper(ctx, packet.Text); err != nil {
			// The turn did not start, so retrying is safe; the next
			// attempt brings a new full snapshot.
			_ = m.context.Fail(ctx, packet.ID, !errors.Is(err, harness.ErrRejected))
			return retry(err)
		}
	}

	a := &turn{requestID: d.RequestID, messageID: msg.MessageID, done: make(chan string, 1)}
	m.mu.Lock()
	m.active = a
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.active == a {
			m.active = nil
		}
		m.mu.Unlock()
	}()
	rec = store.MainTurn{RequestID: d.RequestID, MessageID: msg.MessageID, ThreadID: th.ID(), Status: store.TurnStarting}
	if err := m.app.PutMainTurn(ctx, rec); err != nil {
		if packet != nil {
			_ = m.context.Fail(ctx, packet.ID, false)
		}
		return retry(err)
	}
	turnID, err := th.StartTurn(ctx, harness.TurnInput{Text: input(msg), UserMessageID: msg.MessageID})
	if err != nil {
		rejected := errors.Is(err, harness.ErrRejected)
		if packet != nil {
			_ = m.context.Fail(ctx, packet.ID, !rejected)
		}
		if rejected {
			rec.Status = store.TurnRejected
			if perr := m.app.PutMainTurn(ctx, rec); perr != nil {
				return retry(perr)
			}
			return retry(err)
		}
		// The turn may be running. Take the session down so it cannot
		// overlap the next message's turn; the thread is resumed in a
		// new session.
		if s := m.detach(th); s != nil {
			_ = s.Close()
		}
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "turn/start did not answer; the message may or may not be in the conversation"}
	}
	rec.TurnID, rec.Status = turnID, store.TurnStarted
	m.mu.Lock()
	if a.turnID != "" && a.turnID != turnID {
		m.cfg.Log.WarnContext(ctx, "a different turn started first", "started", a.turnID, "ours", turnID)
	}
	a.turnID = turnID
	if status, ok := m.settled[turnID]; ok {
		delete(m.settled, turnID)
		a.done <- status
	}
	m.mu.Unlock()
	if err := m.app.PutMainTurn(ctx, rec); err != nil {
		m.cfg.Log.ErrorContext(ctx, "could not record the started turn", "turn", turnID, "err", err)
	}
	if packet != nil {
		if err := m.context.Confirm(ctx, packet.ID); err != nil {
			m.cfg.Log.ErrorContext(ctx, "could not confirm the context packet", "packet", packet.ID, "err", err)
		}
	}

	// One turn at a time: the next message waits for this turn to end.
	select {
	case status := <-a.done:
		rec.Status = finalStatus(status)
	case <-ctx.Done():
		return app.DeliveryResult{Outcome: app.Delivered}
	}
	if err := m.app.PutMainTurn(context.WithoutCancel(ctx), rec); err != nil {
		m.cfg.Log.ErrorContext(ctx, "could not record the finished turn", "turn", turnID, "err", err)
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}

// Reconcile implements dispatch.Handler for a delivery a previous process
// left in the middle.
func (m *Manager) Reconcile(ctx context.Context, d app.Delivery) app.DeliveryResult {
	rec, ok, err := m.app.MainTurn(ctx, d.RequestID)
	switch {
	case err != nil:
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "the turn record could not be read"}
	case !ok || rec.Status == store.TurnRejected:
		// The record is written before turn/start, so no record means the
		// turn never started.
		return app.DeliveryResult{Outcome: app.Retry}
	case rec.Status == store.TurnStarting:
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "tend stopped while starting the turn; the message may or may not be in the conversation"}
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}

func retry(err error) app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Retry, Reason: err.Error()}
}

func finalStatus(harnessStatus string) string {
	switch harnessStatus {
	case "completed":
		return store.TurnCompleted
	case store.TurnInterrupted:
		return store.TurnInterrupted
	}
	return store.TurnFailed
}

// input is the turn's user input: the message, prefixed with its identity
// so the agent can refer to it.
func input(msg store.MessageView) string {
	head := fmt.Sprintf("[message %s", msg.MessageID)
	if msg.ReplyTo != "" {
		head += ", reply to " + msg.ReplyTo
	}
	if msg.LastSeenSeq > 0 && msg.LastSeenSeq < msg.Seq-1 {
		// Messages arrived that the sender's client had not shown.
		head += fmt.Sprintf(", written having seen messages up to #%d of #%d", msg.LastSeenSeq, msg.Seq-1)
	}
	return head + "]\n" + msg.Body
}
