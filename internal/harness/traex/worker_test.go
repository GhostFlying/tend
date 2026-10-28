package traex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCompletedRolloutPreservesAttributionAndSupplementRedaction(t *testing.T) {
	for _, kind := range []string{harness.ItemUserMessage, harness.ItemAgentMessage} {
		raw, err := json.Marshal(map[string]any{"type": kind, "text": "private input marker", "content": []any{map[string]string{"text": "private input marker"}}})
		if err != nil {
			t.Fatal(err)
		}
		ev := completedRolloutEvent("thread", harness.Event{ItemID: "item", TurnID: "turn", ItemType: kind, Raw: raw})
		var body map[string]any
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatal(err)
		}
		if ev.Kind != "item.completed" || body["thread_id"] != "thread" || body["turn_id"] != "turn" || body["item_id"] != "item" || body["type"] != kind {
			t.Fatal(ev, body)
		}
		if kind == harness.ItemUserMessage && bytes.Contains(ev.Body, []byte("private input marker")) {
			t.Fatal("supplement plaintext escaped into run rollout")
		}
		if kind == harness.ItemAgentMessage && body["text"] != "private input marker" {
			t.Fatal("assistant prose was lost", body)
		}
	}
}

func TestNativeLifecycleDoesNotRepeatSupplementalInput(t *testing.T) {
	for _, kind := range []harness.EventKind{harness.EventTurnStarted, harness.EventTurnCompleted} {
		raw := json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed","items":[{"id":"private-user","type":"userMessage","content":[{"type":"text","text":"private supplemental marker"}]}]}}`)
		ev := nativeRolloutEvent("thread", harness.Event{Kind: kind, TurnID: "turn", TurnStatus: "completed", Raw: raw})
		if bytes.Contains(ev.Body, []byte("private supplemental marker")) || bytes.Contains(ev.Body, []byte("private-user")) {
			t.Fatal("lifecycle payload leaked user input")
		}
		var body struct {
			ThreadID string `json:"thread_id"`
			TurnID   string `json:"turn_id"`
			Data     struct {
				Turn struct{ ID, Status string } `json:"turn"`
			} `json:"data"`
		}
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.ThreadID != "thread" || body.TurnID != "turn" || body.Data.Turn.ID != "turn" || body.Data.Turn.Status != "completed" {
			t.Fatal(string(ev.Body))
		}
	}
}

func TestWorkerDescendantsRedactInputsBeforePublishingRunLogs(t *testing.T) {
	ws, servers := fakeWorkers(t)
	w, err := ws.Start(t.Context(), harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	// Descendant input can arrive before its ancestor links are known.
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "grandchild", "turnId": "child-turn", "item": map[string]any{"id": "private-item", "type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "PRIVATE_CHILD_INPUT"}}}}})
	f.send(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]any{"id": "grandchild", "source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "child", "depth": 2}}}}}})
	f.send(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]any{"id": "child", "source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "th_w", "depth": 1}}}}}})
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "grandchild", "turnId": "child-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "CHILD_ANSWER_SAFE"}}})
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "grandchild", "turn": map[string]any{"id": "child-turn", "status": "completed", "items": []any{map[string]any{"id": "private-item", "type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "PRIVATE_CHILD_INPUT"}}}}}}})
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]any{"id": "tu_w", "status": "completed"}}})
	children := 0
	answer := false
	for event := range w.Events() {
		if bytes.Contains(event.Body, []byte("PRIVATE_CHILD_INPUT")) {
			t.Fatal("private input escaped through a descendant log", string(event.Body))
		}
		if event.Kind != "native.child" {
			continue
		}
		children++
		var body struct {
			Thread string `json:"thread_id"`
			Turn   string `json:"turn_id"`
			Kind   string `json:"kind"`
		}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Thread != "grandchild" || body.Turn != "child-turn" {
			t.Fatal("descendant lost its original scope", string(event.Body))
		}
		answer = answer || bytes.Contains(event.Body, []byte("CHILD_ANSWER_SAFE"))
	}
	if _, err := w.Wait(); err != nil {
		t.Fatal(err)
	}
	if children != 3 || !answer {
		t.Fatalf("descendant logs=%d answer=%t", children, answer)
	}
}

// fakeWorkers returns Workers that talk to a fake app-server; setup adds
// to what the server answers.
func fakeWorkers(t *testing.T, setup ...func(*fakeServer)) (*Workers, chan *fakeServer) {
	servers := make(chan *fakeServer, 1)
	w := NewWorkers(WorkerOptions{})
	w.start = func(ctx context.Context, opts Options) (*Session, error) {
		// A worker never acts as the owner's agent (D45).
		if !slices.Contains(opts.Env, protocol.AddonEnv+"=off") {
			t.Errorf("worker environment %v does not turn the agent add-on off", opts.Env)
		}
		f, s := newFake(t)
		f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_w"}} })
		f.on(methodTurnStart, func(json.RawMessage) any { return map[string]any{"turn": map[string]any{"id": "tu_w"}} })
		f.on(methodTurnInterrupt, func(json.RawMessage) any { return map[string]any{} })
		for _, fn := range setup {
			fn(f)
		}
		servers <- f
		return s, nil
	}
	return w, servers
}

func TestAcceptanceWorkerReportsVerdict(t *testing.T) {
	ws, servers := fakeWorkers(t)
	w, err := ws.Start(context.Background(), harness.WorkerSpec{Role: protocol.RoleAccept, Dir: "/w", Spec: json.RawMessage(`{"goal":"g","acceptance":["a"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	var start ThreadStartParams
	_ = json.Unmarshal(f.params(methodThreadStart), &start)
	if start.Sandbox != "danger-full-access" || start.ApprovalPolicy != "never" || len(start.DynamicTools) != 1 || start.DynamicTools[0].Name != harness.VerdictTool {
		t.Errorf("thread/start = %s", f.params(methodThreadStart))
	}
	f.send(map[string]any{"id": 5, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "c", "tool": harness.VerdictTool,
		"arguments": map[string]any{"verdict": "passed", "evidence": map[string]any{"tests": "ok"}}}})
	<-f.answers
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "item": map[string]any{"id": "i1", "type": "agentMessage", "text": "All good."}}})
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]any{"id": "tu_w", "status": "completed"}}})
	var events []harness.WorkerEvent
	for event := range w.Events() {
		events = append(events, event)
	}
	var completed struct {
		Thread string `json:"thread_id"`
		Turn   string `json:"turn_id"`
		Item   string `json:"item_id"`
	}
	if len(events) == 0 || json.Unmarshal(events[0].Body, &completed) != nil || completed.Thread != "th_w" || completed.Turn != "tu_w" || completed.Item != "i1" {
		t.Fatalf("completed item lost native attribution: %+v", events)
	}
	var item ThreadItem
	if err := json.Unmarshal(events[0].Body, &item); err != nil || item.Text != "All good." || item.ID != completed.Item {
		t.Fatalf("completed item lost legacy root fields = %s, %v", events[0].Body, err)
	}
	res, err := w.Wait()
	if err != nil || res.Verdict == nil || res.Verdict.Verdict != protocol.VerdictPassed || res.Summary != "All good." || len(events) != 2 || events[1].Kind != "native.turn_completed" {
		t.Errorf("result = %+v, %v (events %+v)", res, err, events)
	}
}

func TestStoppedWorkerInterruptsTurn(t *testing.T) {
	ws, servers := fakeWorkers(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, err := ws.Start(ctx, harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	fence := errors.New("fenced")
	cancel(fence)
	done := make(chan struct{})
	go func() {
		for range w.Events() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
	if _, err := w.Wait(); !errors.Is(err, fence) {
		t.Errorf("Wait = %v, want the cancellation cause", err)
	}
	if f.params(methodTurnInterrupt) == nil {
		t.Error("the turn was not interrupted")
	}
}

// turnText returns the text of the turn a fake server was asked to start.
func turnText(t *testing.T, f *fakeServer) string {
	t.Helper()
	var p TurnStartParams
	if err := json.Unmarshal(f.params(methodTurnStart), &p); err != nil || len(p.Input) != 1 {
		t.Fatalf("turn/start = %s (%v)", f.params(methodTurnStart), err)
	}
	return p.Input[0].Text
}

// recordThread returns an OnThread that keeps the thread, checking that
// no turn has started in it yet, and the server it should check.
func recordThread(t *testing.T, servers chan *fakeServer) (func(string) error, *string, chan *fakeServer) {
	var thread string
	seen := make(chan *fakeServer, 1)
	return func(id string) error {
		f := <-servers
		if f.params(methodTurnStart) != nil {
			t.Error("the thread was reported after its turn started")
		}
		thread = id
		seen <- f
		return nil
	}, &thread, seen
}

func TestWorkerContinuesItsThread(t *testing.T) {
	ws, servers := fakeWorkers(t, func(f *fakeServer) {
		f.on(methodThreadResume, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_old"}} })
	})
	onThread, thread, seen := recordThread(t, servers)
	spec := harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{"goal":"g"}`), Attempt: 2, Thread: "th_old", OnThread: onThread}
	if _, err := ws.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	f := <-seen
	var resume ThreadResumeParams
	if err := json.Unmarshal(f.params(methodThreadResume), &resume); err != nil || resume.ThreadID != "th_old" || f.params(methodThreadStart) != nil {
		t.Errorf("thread/resume = %s, thread/start = %s", f.params(methodThreadResume), f.params(methodThreadStart))
	}
	if got := turnText(t, f); got != harness.Continuation(spec) || *thread != "th_old" {
		t.Errorf("continued with %q in %s", got, *thread)
	}
}

func TestFault_ThreadResumeFailsFallsBackToBrief(t *testing.T) {
	// The fake app-server knows no thread/resume, as when the rollout is
	// gone or traex changed.
	ws, servers := fakeWorkers(t)
	onThread, thread, seen := recordThread(t, servers)
	spec := harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{"goal":"g"}`), Attempt: 2, Thread: "th_old", OnThread: onThread}
	if _, err := ws.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	f := <-seen
	_, brief := harness.Brief(spec)
	if got := turnText(t, f); got != brief || f.params(methodThreadStart) == nil || *thread != "th_w" {
		t.Errorf("fell back to %q in %s", got, *thread)
	}
}

func TestWorkerDoesNotStartAnUnrecordedThread(t *testing.T) {
	ws, servers := fakeWorkers(t)
	failure := errors.New("journal is full")
	_, err := ws.Start(context.Background(), harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{}`),
		OnThread: func(string) error { return failure }})
	if f := <-servers; !errors.Is(err, failure) || f.params(methodTurnStart) != nil {
		t.Errorf("Start = %v, turn/start = %s", err, f.params(methodTurnStart))
	}
}

func TestBothWorkerRolesExposeTaskInputDownload(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			ws, servers := fakeWorkers(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			called := false
			w, err := ws.Start(ctx, harness.WorkerSpec{Role: role, Dir: "/w", Spec: json.RawMessage(`{}`), DownloadFile: func(_ context.Context, id string) (string, error) {
				called = true
				if id != "file_input" {
					return "", errors.New("wrong input")
				}
				return "/scratch/inputs/file_input", nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			var start ThreadStartParams
			_ = json.Unmarshal(f.params(methodThreadStart), &start)
			found := false
			for _, tool := range start.DynamicTools {
				if tool.Name == "download_file" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s worker has no download_file: %s", role, f.params(methodThreadStart))
			}
			f.send(map[string]any{"id": 7, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "download", "tool": "download_file", "arguments": map[string]any{"file_id": "file_input"}}})
			<-f.answers
			if !called {
				t.Error("download tool callback was not dispatched")
			}
			cancel(context.Canceled)
			for range w.Events() {
			}
			if _, err := w.Wait(); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestBothWorkerRolesExposeNegotiatedOwnerQuestion(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			ws, servers := fakeWorkers(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			called := false
			w, err := ws.Start(ctx, harness.WorkerSpec{Role: role, Dir: "/w", Spec: json.RawMessage(`{}`), AskOwner: func(_ context.Context, p protocol.AskOwnerPayload) (protocol.WorkerQuestion, error) {
				called = true
				return protocol.WorkerQuestion{QuestionKey: p.QuestionKey, Status: "answered", Answer: json.RawMessage(`{"text":"continue"}`)}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			var start ThreadStartParams
			_ = json.Unmarshal(f.params(methodThreadStart), &start)
			found := false
			for _, tool := range start.DynamicTools {
				if tool.Name == harness.AskOwnerTool {
					found = true
				}
			}
			if !found {
				t.Error("negotiated question tool missing")
			}
			f.send(map[string]any{"id": 8, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "owner", "tool": harness.AskOwnerTool, "arguments": map[string]any{"question_key": "scope", "question": "Continue?", "evidence": "Investigated scope"}}})
			<-f.answers
			if !called {
				t.Error("question callback not dispatched")
			}
			cancel(context.Canceled)
			for range w.Events() {
			}
			if _, err := w.Wait(); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestBothWorkerRolesExposeScopedObservation(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			ws, servers := fakeWorkers(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			called := false
			w, err := ws.Start(ctx, harness.WorkerSpec{Role: role, Dir: "/w", Spec: json.RawMessage(`{"observation":{"deployment":true}}`), ReadObservation: func(_ context.Context) (protocol.RunObservation, error) {
				called = true
				return protocol.RunObservation{Version: 1}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			var start ThreadStartParams
			_ = json.Unmarshal(f.params(methodThreadStart), &start)
			found := false
			for _, tool := range start.DynamicTools {
				if tool.Name == "read_observation" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s worker has no read_observation: %s", role, f.params(methodThreadStart))
			}
			f.send(map[string]any{"id": 7, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "download", "tool": "read_observation", "arguments": map[string]any{}}})
			<-f.answers
			if !called {
				t.Error("download tool callback was not dispatched")
			}
			cancel(context.Canceled)
			for range w.Events() {
			}
			if _, err := w.Wait(); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
