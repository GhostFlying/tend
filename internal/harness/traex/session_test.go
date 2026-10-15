package traex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// fakeServer plays the app-server side of a connection.
type fakeServer struct {
	t   *testing.T
	in  *bufio.Reader  // what the client writes
	out io.WriteCloser // what the client reads

	mu      sync.Mutex
	replies map[string]func(params json.RawMessage) any
	seen    map[string]json.RawMessage
	answers chan message
}

func newFake(t *testing.T) (*fakeServer, *Session) {
	t.Helper()
	cr, sw := io.Pipe() // server -> client
	sr, cw := io.Pipe() // client -> server
	f := &fakeServer{t: t, in: bufio.NewReader(sr), out: sw, replies: map[string]func(json.RawMessage) any{},
		seen: map[string]json.RawMessage{}, answers: make(chan message, 16)}
	f.on(methodInitialize, func(json.RawMessage) any { return map[string]any{} })
	go f.serve()
	s := newSession(cr, cw, nil)
	t.Cleanup(func() { sw.Close(); s.Close() })
	if err := s.initialize(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	return f, s
}

func (f *fakeServer) on(method string, reply func(json.RawMessage) any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[method] = reply
}

func (f *fakeServer) params(method string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[method]
}

func (f *fakeServer) serve() {
	for {
		line, err := f.in.ReadBytes('\n')
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if m.Method == "" {
			f.answers <- m // the client answering a server request
			continue
		}
		f.mu.Lock()
		f.seen[m.Method] = m.Params
		reply := f.replies[m.Method]
		f.mu.Unlock()
		if m.ID == nil {
			continue
		}
		if reply == nil {
			f.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
			continue
		}
		f.send(map[string]any{"id": m.ID, "result": reply(m.Params)})
	}
}

func (f *fakeServer) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = f.out.Write(append(b, '\n'))
}

func next(t *testing.T, ch <-chan harness.Event) harness.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("events closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return harness.Event{}
}

func TestThreadLifecycle(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_1"}, "model": "m"} })
	f.on(methodTurnStart, func(json.RawMessage) any {
		return map[string]any{"turn": map[string]any{"id": "tu_1", "status": "inProgress"}}
	})

	th, err := s.StartThread(context.Background(), harness.ThreadConfig{Cwd: "/w", Model: "m", Sandbox: "read-only",
		Tools: []harness.ToolSpec{{Name: "tasks_list", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil || th.ID() != "th_1" {
		t.Fatalf("StartThread = %v, %v", th, err)
	}
	var start ThreadStartParams
	if err := json.Unmarshal(f.params(methodThreadStart), &start); err != nil || start.Cwd != "/w" || len(start.DynamicTools) != 1 || start.DynamicTools[0].Type != "function" {
		t.Errorf("thread/start params = %s", f.params(methodThreadStart))
	}

	turn, err := th.StartTurn(context.Background(), harness.TurnInput{Text: "hello", UserMessageID: "msg_1"})
	if err != nil || turn != "tu_1" {
		t.Fatalf("StartTurn = %q, %v", turn, err)
	}
	var ts TurnStartParams
	if err := json.Unmarshal(f.params(methodTurnStart), &ts); err != nil || ts.Input[0].Text != "hello" || ts.UserMessageID != "msg_1" {
		t.Errorf("turn/start params = %s", f.params(methodTurnStart))
	}

	// Events of another thread are not delivered here.
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "th_other", "turnId": "x", "item": map[string]any{"id": "i0", "type": "agentMessage", "text": "nope"}}})
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "th_1", "turnId": "tu_1", "item": map[string]any{"id": "i1", "type": "agentMessage", "text": "hi"}}})
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "th_1", "turnId": "tu_1", "item": map[string]any{"id": "i2", "type": "contextCompaction"}}})
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_1", "turn": map[string]any{"id": "tu_1", "status": "completed"}}})

	if ev := next(t, th.Events()); ev.Kind != harness.EventItemCompleted || ev.ItemID != "i1" || ev.Text != "hi" {
		t.Errorf("first event = %+v", ev)
	}
	if ev := next(t, th.Events()); ev.ItemType != harness.ItemContextCompaction {
		t.Errorf("second event = %+v", ev)
	}
	if ev := next(t, th.Events()); ev.Kind != harness.EventTurnCompleted || ev.TurnStatus != "completed" {
		t.Errorf("third event = %+v", ev)
	}
}

func TestInjectDeveloperItems(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadResume, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_1"}} })
	f.on(methodThreadInject, func(json.RawMessage) any { return map[string]any{} })
	th, err := s.ResumeThread(context.Background(), "th_1", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := th.InjectDeveloper(context.Background(), "[tend context packet p1]", "second"); err != nil {
		t.Fatal(err)
	}
	var p struct {
		ThreadID string `json:"threadId"`
		Items    []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(f.params(methodThreadInject), &p); err != nil || len(p.Items) != 2 {
		t.Fatalf("inject params = %s", f.params(methodThreadInject))
	}
	if it := p.Items[0]; it.Type != "message" || it.Role != "developer" || it.Content[0].Type != "input_text" || it.Content[0].Text != "[tend context packet p1]" {
		t.Errorf("item = %+v", it)
	}
}

func TestToolCallsReachTheirThread(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_1"}} })
	got := make(chan harness.ToolCall, 1)
	if _, err := s.StartThread(context.Background(), harness.ThreadConfig{OnToolCall: func(_ context.Context, c harness.ToolCall) harness.ToolResult {
		got <- c
		return harness.ToolResult{Success: true, Text: "ok:" + string(c.Arguments)}
	}}); err != nil {
		t.Fatal(err)
	}
	f.send(map[string]any{"id": 7, "method": methodToolCall, "params": map[string]any{
		"threadId": "th_1", "turnId": "tu_1", "callId": "call_9", "namespace": nil, "tool": "tasks_list", "arguments": map[string]any{"q": 1},
	}})
	call := <-got
	if call.CallID != "call_9" || call.TurnID != "tu_1" || call.Tool != "tasks_list" {
		t.Errorf("call = %+v", call)
	}
	ans := <-f.answers
	var res DynamicToolCallResponse
	if err := json.Unmarshal(ans.Result, &res); err != nil || !res.Success || res.ContentItems[0].Type != "inputText" || res.ContentItems[0].Text != `ok:{"q":1}` {
		t.Errorf("answer = %s", ans.Result)
	}

	f.send(map[string]any{"id": 8, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
	if ans := <-f.answers; ans.Error == nil || ans.Error.Code != -32601 {
		t.Errorf("unknown request answered with %+v", ans)
	}
}

func TestServerExitEndsSession(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_1"}} })
	th, err := s.StartThread(context.Background(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	f.out.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not notice the exit")
	}
	if _, err := th.StartTurn(context.Background(), harness.TurnInput{Text: "x"}); !errors.Is(err, ErrClosed) {
		t.Errorf("StartTurn after exit = %v, want ErrClosed", err)
	}
	if _, ok := <-th.Events(); ok {
		t.Error("events still open after exit")
	}
}

func TestEventsBeforeRegistrationAreKept(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any {
		// The notification reaches the client before the response.
		f.send(map[string]any{"method": notifyTurnStarted, "params": map[string]any{"threadId": "th_1", "turn": map[string]any{"id": "tu_0", "status": "inProgress"}}})
		return map[string]any{"thread": map[string]any{"id": "th_1"}}
	})
	th, err := s.StartThread(context.Background(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ev := next(t, th.Events()); ev.Kind != harness.EventTurnStarted || ev.TurnID != "tu_0" {
		t.Errorf("event = %+v", ev)
	}
}

func TestCloseReleasesAbandonedConsumers(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_1"}} })
	th, err := s.StartThread(context.Background(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		f.send(map[string]any{"method": notifyTurnStarted, "params": map[string]any{"threadId": "th_1", "turn": map[string]any{"id": i}}})
	}
	time.Sleep(50 * time.Millisecond)
	s.Close()
	f.out.Close()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-th.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the event stream stayed open after Close")
		}
	}
}

func TestCallGivesUpWhenTheServerStopsReading(t *testing.T) {
	cr, _ := io.Pipe()
	_, cw := io.Pipe() // nobody reads what the client writes
	c := newConn(cr, cw, handlers{notify: func(string, json.RawMessage) {}}, slogDiscard())
	// The first write occupies the writer goroutine; the second must time
	// out instead of hanging.
	go func() { _ = c.call(context.Background(), "a", nil, nil) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.call(ctx, "b", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("call = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("call took %v", d)
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }
