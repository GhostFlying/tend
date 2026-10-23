package traex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestIsolatedForkUsesOwnHandlerAndReleasesOnlyFork(t *testing.T) {
	f, s := newFake(t)
	ctx := context.Background()
	f.on(methodThreadStart, func(json.RawMessage) any {
		return ThreadStartResponse{Thread: Thread{ID: "source", Path: forkPath("source-rollout")}}
	})
	source, err := s.StartThread(ctx, harness.ThreadConfig{OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
		t.Error("source handler called for summary")
		return harness.ToolResult{Success: true}
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.on(methodThreadFork, func(raw json.RawMessage) any {
		var p ThreadForkParams
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p.ThreadID != "source" || !p.Ephemeral || !p.ExcludeTurns || p.DeveloperInstructions != "summary only" {
			t.Errorf("fork params %+v", p)
		}
		return ThreadStartResponse{Thread: Thread{ID: "summary"}, Model: "summary-model"}
	})
	calls := make(chan harness.ToolCall, 1)
	fork, err := s.ForkThread(ctx, source.ID(), harness.ThreadConfig{DeveloperInstructions: "summary only", OnToolCall: func(_ context.Context, c harness.ToolCall) harness.ToolResult {
		calls <- c
		return harness.ToolResult{Text: "summary tools disabled"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.send(map[string]any{"id": 7, "method": methodToolCall, "params": map[string]any{"threadId": fork.ID(), "turnId": "summary-turn", "callId": "old-call", "tool": "old_mutation", "arguments": map[string]any{}}})
	select {
	case c := <-calls:
		if c.ThreadID != fork.ID() {
			t.Fatal(c)
		}
	case <-time.After(time.Second):
		t.Fatal("fork tool handler not called")
	}
	select {
	case answer := <-f.answers:
		var response DynamicToolCallResponse
		if err := json.Unmarshal(answer.Result, &response); err != nil {
			t.Fatal(err)
		}
		if response.Success {
			t.Fatal("summary tool unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("missing rejection response")
	}
	if err := s.ReleaseThread(ctx, source.ID()); !errors.Is(err, harness.ErrRejected) {
		t.Fatalf("source release: %v", err)
	}
	if f.params(methodThreadUnsubscribe) != nil {
		t.Fatal("source unsubscribed")
	}
	f.on(methodThreadUnsubscribe, func(raw json.RawMessage) any {
		var p ThreadUnsubscribeParams
		_ = json.Unmarshal(raw, &p)
		if p.ThreadID != "summary" {
			t.Errorf("unsubscribed %s", p.ThreadID)
		}
		return ThreadUnsubscribeResponse{Status: "unsubscribed"}
	})
	if err := s.ReleaseThread(ctx, fork.ID()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseThread(ctx, fork.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-fork.Events():
		if ok {
			t.Fatal("events remain open")
		}
	case <-time.After(time.Second):
		t.Fatal("released fork event pump leaked")
	}
	if _, err := fork.StartTurn(ctx, harness.TurnInput{Text: "late"}); !errors.Is(err, harness.ErrRejected) {
		t.Fatalf("released fork start: %v", err)
	}
	if path := source.(harness.ThreadLocation).RolloutPath(); path != "source-rollout" {
		t.Fatal(path)
	}
	if path := fork.(harness.ThreadLocation).RolloutPath(); path != "" {
		t.Fatal(path)
	}
}

func TestResumeThreadLocation(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadResume, func(json.RawMessage) any {
		return ThreadStartResponse{Thread: Thread{ID: "source", Path: forkPath("resumed-rollout")}}
	})
	th, err := s.ResumeThread(context.Background(), "source", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := th.(harness.ThreadLocation).RolloutPath(); got != "resumed-rollout" {
		t.Fatal(got)
	}
}

func TestForkDoesNotRegisterInvalidSourceIdentity(t *testing.T) {
	f, s := newFake(t)
	source := s.register("source", nil)
	f.on(methodThreadFork, func(json.RawMessage) any { return ThreadStartResponse{Thread: Thread{ID: "source"}} })
	if _, err := s.ForkThread(context.Background(), "source", harness.ThreadConfig{}); err == nil {
		t.Fatal("accepted source identity")
	}
	if s.thread("source") != source {
		t.Fatal("source registration replaced")
	}
}

func forkPath(s string) *string { return &s }

func TestReleaseForkRetainsRetryAfterRejectedUnsubscribe(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadFork, func(json.RawMessage) any { return ThreadStartResponse{Thread: Thread{ID: "summary"}} })
	th, err := s.ForkThread(context.Background(), "source", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseThread(context.Background(), th.ID()); !errors.Is(err, harness.ErrRejected) {
		t.Fatalf("unsubscribe rejection %v", err)
	}
	if s.thread(th.ID()).toolsReleased() {
		t.Fatal("failed unsubscribe was treated as released")
	}
	f.on(methodThreadUnsubscribe, func(json.RawMessage) any { return ThreadUnsubscribeResponse{Status: "notLoaded"} })
	if err := s.ReleaseThread(context.Background(), th.ID()); err != nil {
		t.Fatal(err)
	}
	if !s.thread(th.ID()).toolsReleased() {
		t.Fatal("successful retry did not release")
	}
}
