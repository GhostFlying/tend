package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestRepeatedResumePreservesEventStreamAndClosesEveryHandle(t *testing.T) {
	f, s := newFake(t)
	ctx := context.Background()
	f.on(methodThreadStart, func(json.RawMessage) any { return ThreadStartResponse{Thread: Thread{ID: "target"}} })
	original, err := s.StartThread(ctx, harness.ThreadConfig{OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
		t.Error("obsolete handler used")
		return harness.ToolResult{}
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.on(methodThreadResume, func(json.RawMessage) any {
		return ThreadStartResponse{Thread: Thread{ID: "target", Path: forkPath("resumed-path")}}
	})
	handles := []harness.Thread{original}
	for i := 0; i < 12; i++ {
		f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "target", "turnId": "owner-turn", "item": map[string]any{"id": fmt.Sprint("item-", i), "type": harness.ItemAgentMessage, "text": fmt.Sprint(i)}}})
		resumed, err := s.ResumeThread(ctx, "target", harness.ThreadConfig{OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
			return harness.ToolResult{Success: true, Text: "current-handler"}
		}})
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, resumed)
		if resumed != original || resumed.Events() != original.Events() {
			t.Fatal("resume replaced the live registration/event stream")
		}
	}
	for i := 0; i < 12; i++ {
		select {
		case ev := <-original.Events():
			if ev.ItemID != fmt.Sprint("item-", i) {
				t.Fatalf("queued event lost/reordered: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("queued event disappeared during repeated resume")
		}
	}
	raw, _ := json.Marshal(DynamicToolCallParams{ThreadID: "target", TurnID: "owner-turn", CallID: "call", Tool: "read", Arguments: json.RawMessage(`{}`)})
	response, err := s.serverRequest(ctx, methodToolCall, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !response.(DynamicToolCallResponse).Success {
		t.Fatal("confirmed resume did not install current handler")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, th := range handles {
		select {
		case _, ok := <-th.Events():
			if ok {
				t.Fatal("unexpected event after closing drained session")
			}
		case <-time.After(time.Second):
			t.Fatal("an old resumed event pump survived Session.Close")
		}
	}
}

func TestRejectedResumeKeepsExistingRegistrationAndHandler(t *testing.T) {
	_, s := newFake(t)
	ctx := context.Background()
	original := s.register("target", func(context.Context, harness.ToolCall) harness.ToolResult {
		return harness.ToolResult{Success: true, Text: "accepted-original"}
	})
	if _, err := s.ResumeThread(ctx, "target", harness.ThreadConfig{OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
		t.Error("rejected handler installed")
		return harness.ToolResult{}
	}}); !errors.Is(err, harness.ErrRejected) {
		t.Fatalf("resume error %v", err)
	}
	if s.thread("target") != original {
		t.Fatal("rejected resume discarded live registration")
	}
	result := original.toolHandler()(ctx, harness.ToolCall{})
	if !result.Success || result.Text != "accepted-original" {
		t.Fatal("rejected resume changed accepted handler")
	}
	original.push(harness.Event{ItemID: "still-live"})
	select {
	case ev := <-original.Events():
		if ev.ItemID != "still-live" {
			t.Fatal(ev)
		}
	case <-time.After(time.Second):
		t.Fatal("rejected resume closed existing event stream")
	}
}

func TestFailedFirstResumeRemovesAndStopsOnlyNewRegistration(t *testing.T) {
	f, s := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan *thread, 1)
	release := make(chan struct{})
	defer close(release)
	f.on(methodThreadResume, func(json.RawMessage) any {
		entered <- s.thread("new-target")
		<-release
		return ThreadStartResponse{Thread: Thread{ID: "new-target"}}
	})
	finished := make(chan error, 1)
	go func() { _, err := s.ResumeThread(ctx, "new-target", harness.ThreadConfig{}); finished <- err }()
	var pending *thread
	select {
	case pending = <-entered:
	case <-time.After(time.Second):
		t.Fatal("resume request not observed")
	}
	if pending == nil {
		t.Fatal("first resume was not registered before native response")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled resume did not return")
	}
	if s.thread("new-target") != nil {
		t.Fatal("failed first resume remained registered")
	}
	select {
	case _, ok := <-pending.Events():
		if ok {
			t.Fatal("unexpected pending event")
		}
	case <-time.After(time.Second):
		t.Fatal("failed first resume leaked event pump")
	}
}

func TestThreadLifecycleGateCancellationAndClose(t *testing.T) {
	for _, kind := range []string{"cancel", "close"} {
		t.Run(kind, func(t *testing.T) {
			_, s := newFake(t)
			unlock, err := s.lockThreadLifecycle(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.ResumeThread(ctx, "waiting-target", harness.ThreadConfig{}); done <- err }()
			if kind == "cancel" {
				cancel()
			} else {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("blocked lifecycle request succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("lifecycle gate waiter survived cancellation/session close")
			}
			if s.thread("waiting-target") != nil {
				t.Fatal("cancelled gate waiter registered a new thread")
			}
		})
	}
}

func TestResumeCannotReactivateReleasedFork(t *testing.T) {
	f, s := newFake(t)
	ctx := context.Background()
	f.on(methodThreadFork, func(json.RawMessage) any { return ThreadStartResponse{Thread: Thread{ID: "summary-fork"}} })
	fork, err := s.ForkThread(ctx, "source", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	f.on(methodThreadUnsubscribe, func(json.RawMessage) any { return ThreadUnsubscribeResponse{Status: "unsubscribed"} })
	if err := s.ReleaseThread(ctx, fork.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeThread(ctx, fork.ID(), harness.ThreadConfig{}); !errors.Is(err, harness.ErrRejected) {
		t.Fatalf("released fork resume: %v", err)
	}
	if f.params(methodThreadResume) != nil {
		t.Fatal("released fork resubscribed at native runtime")
	}
	if !s.thread(fork.ID()).fork || !s.thread(fork.ID()).toolsReleased() {
		t.Fatal("resume reset fork lifecycle flags")
	}
}
