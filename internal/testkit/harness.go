package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Harness is a scriptable harness that survives "restarts": Open returns a
// new session each time, sharing the threads the harness persisted, as an
// app-server's thread store does.
type Harness struct {
	// Turn plays a turn. It runs on its own goroutine after the turn
	// started event; the turn completes when it returns, with the status
	// it returns, unless it returns "" because it completed the turn
	// itself.
	Turn func(t *FakeThread, turnID string, in harness.TurnInput) string
	// StartTurnErr, when set, decides whether turn/start fails.
	StartTurnErr func(in harness.TurnInput) error
	// InterruptErr, when set, decides whether turn/interrupt fails; a
	// failed one stops nothing.
	InterruptErr func(ctx context.Context, turnID string) error
	// BeforeAnswer, when set, runs inside turn/start before it returns,
	// so its events can reach the consumer ahead of the response.
	BeforeAnswer func(t *FakeThread, turnID string)

	mu       sync.Mutex
	persist  map[string]bool
	sessions []*FakeSession
	starts   []harness.TurnInput
	injected map[string][]string
	resumed  int
	nextID   int
	nextTurn int
}

// NewHarness returns a Harness whose turns complete without doing anything
// unless Turn is set.
func NewHarness() *Harness {
	return &Harness{persist: map[string]bool{}, injected: map[string][]string{}}
}

// Open starts a new session, like starting the harness process.
func (h *Harness) Open(context.Context) (harness.Session, error) {
	s := &FakeSession{h: h, done: make(chan struct{}), threads: map[string]*FakeThread{}}
	h.mu.Lock()
	h.sessions = append(h.sessions, s)
	h.mu.Unlock()
	return s, nil
}

// Crash ends the newest session as if the process died.
func (h *Harness) Crash() {
	h.mu.Lock()
	s := h.sessions[len(h.sessions)-1]
	h.mu.Unlock()
	s.end()
}

// Starts returns every turn input that reached turn/start.
func (h *Harness) Starts() []harness.TurnInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]harness.TurnInput(nil), h.starts...)
}

// Injected returns the developer messages injected into a thread.
func (h *Harness) Injected(threadID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.injected[threadID]...)
}

// Sessions counts the sessions opened.
func (h *Harness) Sessions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// Resumed counts thread resumptions.
func (h *Harness) Resumed() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resumed
}

// FakeSession is one session of a Harness.
type FakeSession struct {
	h       *Harness
	once    sync.Once
	done    chan struct{}
	mu      sync.Mutex
	threads map[string]*FakeThread
}

func (s *FakeSession) end() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, t := range s.threads {
			t.close()
		}
	})
}

// Done implements harness.Session.
func (s *FakeSession) Done() <-chan struct{} { return s.done }

// Close implements harness.Session.
func (s *FakeSession) Close() error {
	s.end()
	return nil
}

func (s *FakeSession) thread(id string, cfg harness.ThreadConfig) *FakeThread {
	t := &FakeThread{id: id, Config: cfg, s: s, wake: make(chan struct{}, 1), events: make(chan harness.Event)}
	go t.pump()
	s.mu.Lock()
	s.threads[id] = t
	s.mu.Unlock()
	return t
}

// StartThread implements harness.Session.
func (s *FakeSession) StartThread(_ context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.h.mu.Lock()
	s.h.nextID++
	id := fmt.Sprintf("th_%d", s.h.nextID)
	s.h.persist[id] = true
	s.h.mu.Unlock()
	return s.thread(id, cfg), nil
}

// ResumeThread implements harness.Session.
func (s *FakeSession) ResumeThread(_ context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.h.mu.Lock()
	known := s.h.persist[id]
	if known {
		s.h.resumed++
	}
	s.h.mu.Unlock()
	if !known {
		return nil, fmt.Errorf("thread %s: %w", id, harness.ErrRejected)
	}
	return s.thread(id, cfg), nil
}

// FakeThread is a thread of a FakeSession.
type FakeThread struct {
	id     string
	Config harness.ThreadConfig
	s      *FakeSession

	mu     sync.Mutex
	queue  []harness.Event
	ended  bool
	wake   chan struct{}
	events chan harness.Event
}

// ID implements harness.Thread.
func (t *FakeThread) ID() string { return t.id }

// Events implements harness.Thread.
func (t *FakeThread) Events() <-chan harness.Event { return t.events }

// InjectDeveloper implements harness.Thread.
func (t *FakeThread) InjectDeveloper(_ context.Context, texts ...string) error {
	h := t.s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.injected[t.id] = append(h.injected[t.id], texts...)
	return nil
}

// StartTurn implements harness.Thread.
func (t *FakeThread) StartTurn(_ context.Context, in harness.TurnInput) (string, error) {
	h := t.s.h
	select {
	case <-t.s.done:
		return "", errors.New("fake harness: session ended")
	default:
	}
	if h.StartTurnErr != nil {
		if err := h.StartTurnErr(in); err != nil {
			return "", err
		}
	}
	h.mu.Lock()
	h.starts = append(h.starts, in)
	// Turn IDs stay unique across sessions, as a persisted thread's are.
	h.nextTurn++
	id := fmt.Sprintf("%s_tu_%d", t.id, h.nextTurn)
	h.mu.Unlock()
	if h.BeforeAnswer != nil {
		t.Emit(harness.Event{Kind: harness.EventTurnStarted, TurnID: id})
		h.BeforeAnswer(t, id)
		time.Sleep(50 * time.Millisecond) // let the consumer see them first
	}
	go func() {
		if h.BeforeAnswer == nil {
			t.Emit(harness.Event{Kind: harness.EventTurnStarted, TurnID: id})
		}
		status := "completed"
		if h.Turn != nil {
			status = h.Turn(t, id, in)
		}
		if status != "" {
			t.Emit(harness.Event{Kind: harness.EventTurnCompleted, TurnID: id, TurnStatus: status})
		}
	}()
	return id, nil
}

// Interrupt implements harness.Thread.
func (t *FakeThread) Interrupt(ctx context.Context, turnID string) error {
	if f := t.s.h.InterruptErr; f != nil {
		if err := f(ctx, turnID); err != nil {
			return err
		}
	}
	t.Emit(harness.Event{Kind: harness.EventTurnCompleted, TurnID: turnID, TurnStatus: "interrupted"})
	return nil
}

// Emit delivers an event on the thread.
func (t *FakeThread) Emit(ev harness.Event) {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.queue = append(t.queue, ev)
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Say emits an agent message item.
func (t *FakeThread) Say(turnID, itemID, text string) {
	t.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: itemID, ItemType: harness.ItemAgentMessage, Text: text})
}

// UseTool emits the item event of a tool use: started, or completed when
// the use is no longer running.
func (t *FakeThread) UseTool(turnID, itemID string, u harness.ToolUse) {
	kind := harness.EventItemStarted
	if u.Status != protocol.ToolRunning {
		kind = harness.EventItemCompleted
	}
	t.Emit(harness.Event{Kind: kind, TurnID: turnID, ItemID: itemID, ItemType: "dynamicToolCall", Tool: &u})
}

// Compact emits a context compaction item.
func (t *FakeThread) Compact(turnID string) {
	t.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turnID, ItemID: turnID + "_compaction", ItemType: harness.ItemContextCompaction})
}

// CallTool calls one of the thread's tools as the model would.
func (t *FakeThread) CallTool(ctx context.Context, turnID, callID, tool string, args any) harness.ToolResult {
	raw, _ := json.Marshal(args)
	return t.Config.OnToolCall(ctx, harness.ToolCall{ThreadID: t.id, TurnID: turnID, CallID: callID, Namespace: "tend", Tool: tool, Arguments: raw})
}

func (t *FakeThread) close() {
	t.mu.Lock()
	t.ended = true
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *FakeThread) pump() {
	defer close(t.events)
	for {
		t.mu.Lock()
		if len(t.queue) == 0 {
			ended := t.ended
			t.mu.Unlock()
			if ended {
				return
			}
			<-t.wake
			continue
		}
		ev := t.queue[0]
		t.queue = t.queue[1:]
		t.mu.Unlock()
		select {
		case t.events <- ev:
		case <-t.s.done:
			return
		}
	}
}
