//go:build integration

package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// TestIntegrationEphemeralSummaryFork uses only a loopback fake Responses
// endpoint. It never sends a request to a real model or reads owner credentials.
func TestIntegrationEphemeralSummaryFork(t *testing.T) {
	bin, err := exec.LookPath("traex")
	if err != nil {
		t.Skip("traex not installed")
	}
	var mu sync.Mutex
	var requests []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, string(b))
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) { data, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", data) }
		send(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("fake-", n)}})
		if n == 1 {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": "old-tool-call", "name": "legacy_tend_mutation", "arguments": "{}"}})
		} else {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "summary-message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "SUMMARY_OUTPUT_MARKER"}}}})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("fake-", n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer backend.Close()
	home, cwd := t.TempDir(), t.TempDir()
	config := fmt.Sprintf("model_provider = \"summary_probe\"\nthread_unload_delay_secs = 0\n[model_providers.summary_probe]\nname = \"isolated summary probe\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{Bin: bin, Dir: cwd, Env: []string{"TRAE_HOME=" + home}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := s.StartThread(ctx, harness.ThreadConfig{Cwd: cwd, Model: "probe-model", ApprovalPolicy: "never", Sandbox: "read-only", Tools: []harness.ToolSpec{{Name: "legacy_tend_mutation", Description: "must be denied in summary fork", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}, OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
		t.Error("source callback executed for fork")
		return harness.ToolResult{Success: true}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.InjectDeveloper(ctx, "SOURCE_HISTORY_MARKER"); err != nil {
		t.Fatal(err)
	}
	path := source.(harness.ThreadLocation).RolloutPath()
	if path == "" {
		t.Fatal("source path missing")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan harness.ToolCall, 1)
	fork, err := s.ForkThread(ctx, source.ID(), harness.ThreadConfig{Cwd: cwd, Model: "probe-model", ApprovalPolicy: "never", Sandbox: "read-only", DeveloperInstructions: "summary only", OnToolCall: func(_ context.Context, c harness.ToolCall) harness.ToolResult {
		called <- c
		return harness.ToolResult{Text: "SUMMARY_TOOLS_DISABLED"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.ReleaseThread(cleanup, fork.ID()); err != nil {
			t.Error(err)
		}
	}()
	if fork.(harness.ThreadLocation).RolloutPath() != "" {
		t.Fatal("ephemeral fork unexpectedly has rollout path")
	}
	if _, err := fork.StartTurn(ctx, harness.TurnInput{Text: "SUMMARY_PROMPT_MARKER"}); err != nil {
		t.Fatal(err)
	}
	completed := false
	for !completed {
		select {
		case ev := <-fork.Events():
			if ev.Kind == harness.EventTurnCompleted {
				if ev.TurnStatus != "completed" {
					t.Fatalf("summary status %s", ev.TurnStatus)
				}
				completed = true
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case c := <-called:
		if c.ThreadID != fork.ID() || c.Tool != "legacy_tend_mutation" {
			t.Fatalf("wrong tool identity %+v", c)
		}
	default:
		t.Fatal("inherited tool callback was not intercepted")
	}
	mu.Lock()
	captured := append([]string(nil), requests...)
	mu.Unlock()
	if len(captured) != 2 || !strings.Contains(captured[0], "SOURCE_HISTORY_MARKER") || !strings.Contains(captured[1], "SUMMARY_TOOLS_DISABLED") {
		t.Fatalf("unexpected local model exchange (%d requests)", len(captured))
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source rollout changed during summary")
	}
	if strings.Contains(string(after), "SUMMARY_PROMPT_MARKER") || strings.Contains(string(after), "SUMMARY_OUTPUT_MARKER") {
		t.Fatal("summary contaminated source rollout")
	}
	if err := s.ReleaseThread(ctx, fork.ID()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var loaded struct {
			Data []string `json:"data"`
		}
		if err := s.conn.call(ctx, "thread/loaded/list", struct{}{}, &loaded); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range loaded.Data {
			if id == fork.ID() {
				found = true
			}
		}
		if !found {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("unsubscribed ephemeral fork remained loaded")
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var sourceRead ThreadStartResponse
	if err := s.conn.call(ctx, "thread/read", map[string]any{"threadId": source.ID(), "includeTurns": false}, &sourceRead); err != nil {
		t.Fatal(err)
	}
	if sourceRead.Thread.ID != source.ID() {
		t.Fatal("source no longer accessible")
	}
	clean, err := s.StartThread(ctx, harness.ThreadConfig{Cwd: cwd, Model: "probe-model", Ephemeral: true, ApprovalPolicy: "never", Sandbox: "read-only", DeveloperInstructions: "clean summary merger"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseThread(context.WithoutCancel(ctx), clean.ID())
	if clean.(harness.ThreadLocation).RolloutPath() != "" {
		t.Fatal("clean ephemeral thread persisted a rollout")
	}
	if _, status := turn(t, clean, "CLEAN_MERGE_MARKER"); status != "completed" {
		t.Fatal(status)
	}
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if strings.Contains(last, "SOURCE_HISTORY_MARKER") || strings.Contains(last, "legacy_tend_mutation") || !strings.Contains(last, "CLEAN_MERGE_MARKER") {
		t.Fatal("second stage inherited source history or tools")
	}
	if err := s.ReleaseThread(ctx, clean.ID()); err != nil {
		t.Fatal(err)
	}

	t.Log("loopback fake model: source history inherited; summary tool denied; source unchanged; ephemeral fork unsubscribed")
}
