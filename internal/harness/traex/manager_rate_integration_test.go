//go:build integration

package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// The provider is entirely synthetic; this test never contacts a model service.
func TestIntegration_TraexRetainedToolsAndOpaqueRateLimit(t *testing.T) {
	if os.Getenv("TEND_PROBE_NATIVE_EXECUTION") != "1" || os.Getenv("TEND_MANAGER_CLI") != "traex" {
		t.Skip("requires isolated native Traex opt-in")
	}
	bin := os.Getenv("TEND_TRAEX_BIN")
	if bin == "" {
		var err error
		bin, err = exec.LookPath("traex")
		if err != nil {
			t.Skip("Traex binary unavailable")
		}
	}
	var mu sync.Mutex
	var requests []map[string]any
	var block atomic.Bool
	block.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		n := len(requests)
		mu.Unlock()
		if n >= 2 && block.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"synthetic rate limit","code":"rate_limit_exceeded"}}`))
			return
		}
		item := map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("msg_%d", n), "content": []any{map[string]any{"type": "output_text", "text": "remaining work complete"}}}
		if n == 1 {
			item = map[string]any{"type": "function_call", "call_id": "operation_once", "name": "probe_once", "arguments": "{}"}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("resp_%d", n)}},
			{"type": "response.output_item.done", "item": item},
			{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", n), "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			raw, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], raw)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	config := fmt.Sprintf(`permission_mode="bypass_permissions"
model_provider="fixture"
model="gpt-5.1-codex-mini"
[model_providers.fixture]
name="Isolated test provider"
base_url=%q
wire_api="responses"
requires_openai_auth=false
request_max_retries=0
stream_max_retries=1
supports_websockets=false
[features]
enable_request_compression=false
memories=false
plugins=false
apps=false
shell_tool=false
code_mode=false
js_repl=false
`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	options := Options{Bin: bin, Dir: home, Env: []string{"TRAE_HOME=" + home, "CODEX_HOME=" + home, "TEND_ADDON=off", "OPENAI_API_KEY=", "CODEX_API_KEY="}, Version: "recovery-integration"}
	s, err := Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.codex {
		t.Fatal("unexpected Codex dialect")
	}
	var calls atomic.Int32
	configThread := harness.ThreadConfig{Config: map[string]any{"harness_mode": "codex", "model_harness": map[string]string{"gpt-5.1-codex-mini": "codex"}, "model_tool_modes": map[string]string{"gpt-5.1-codex-mini": "direct"}}, Cwd: home, Model: "gpt-5.1-codex-mini", Sandbox: "read-only", ApprovalPolicy: "never", Tools: []harness.ToolSpec{{Name: "probe_once", Description: "A one-time fixture operation", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}, OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
		calls.Add(1)
		return harness.ToolResult{Success: true, Text: "TOOL_ALREADY_DONE"}
	}}
	th, err := s.StartThread(ctx, configThread)
	if err != nil {
		t.Fatal(err)
	}
	first, err := th.StartTurn(ctx, harness.TurnInput{Text: "Perform the operation once and finish.", UserMessageID: "owner-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	waitNative := func(id string) {
		t.Helper()
		for {
			select {
			case ev := <-th.Events():
				if ev.Failure != nil {
					t.Logf("native failure kind=%s evidence=%+v", ev.Kind, ev.Failure)
				}
				if ev.Kind == harness.EventTurnCompleted && ev.TurnID == id {
					return
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	waitNative(first)
	receipt, found, err := th.(harness.TurnReader).FindTurn(ctx, "owner-fixture", "")
	if err != nil || !found || receipt.Status != "failed" || receipt.Failure == nil {
		t.Fatalf("failed native receipt: %+v found=%t err=%v", receipt, found, err)
	}
	t.Logf("terminal Traex failure code=%s http_status=%d automatic_continuation=%t", receipt.Failure.Code, receipt.Failure.HTTPStatus, receipt.ContinuationSafe)
	if receipt.ContinuationSafe {
		t.Fatal("unverified native dialect advertised automatic continuation safety")
	}
	block.Store(false)
	// This explicitly tests the native empty-input primitive. The coordinator
	// still requires typed rate-limit and continuation-safety evidence before
	// scheduling an automatic recovery; error prose never supplies that authority.
	// A waiting background manager releases its process. Resume the same
	// native thread in a fresh process before the scheduled continuation.
	threadID := th.ID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	th, err = resumed.ResumeThread(ctx, threadID, configThread)
	if err != nil {
		t.Fatal(err)
	}
	if th.ID() != threadID {
		t.Fatalf("resumed a different thread: %s != %s", th.ID(), threadID)
	}
	second, err := th.StartTurn(ctx, harness.TurnInput{Continue: true})
	if err != nil {
		t.Fatal(err)
	}
	waitNative(second)
	done, found, err := th.(harness.TurnReader).FindTurn(ctx, "", second)
	if err != nil || !found || done.Status != "completed" {
		t.Fatalf("continuation: %+v %t %v", done, found, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 3 || calls.Load() != 1 {
		t.Fatalf("requests=%d tool calls=%d", len(requests), calls.Load())
	}
	count := func(body map[string]any) (users, outputs int) {
		for _, v := range body["input"].([]any) {
			item := v.(map[string]any)
			if item["role"] == "user" {
				users++
			}
			if item["type"] == "function_call_output" {
				outputs++
			}
		}
		return
	}
	before, _ := count(requests[len(requests)-2])
	after, outputs := count(requests[len(requests)-1])
	if before != after || outputs != 1 {
		t.Fatalf("replayed input or lost tool result: users %d -> %d; outputs=%d", before, after, outputs)
	}
}
