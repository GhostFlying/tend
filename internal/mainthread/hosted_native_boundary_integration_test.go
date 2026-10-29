//go:build integration

package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
)

// Uses the actual hosted adapter, a deterministic local provider, and empty
// temporary homes. No model service, owner configuration, or ledger is used.
func TestIntegrationHostedNativeBoundary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hosted role adapter is currently supported on Linux only")
	}
	if os.Getenv("TEND_PROBE_NATIVE_EXECUTION") != "1" {
		t.Skip("requires explicit local CLI opt-in")
	}
	bin, err := exec.LookPath("traex")
	if err != nil {
		t.Skip("traex not installed")
	}
	for _, native := range []string{"exec_command", "apply_patch", "spawn_agent", "send_input", "resume_agent", "wait_agent", "close_agent"} {
		t.Run(native, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			var mu sync.Mutex
			requests, dynamic := 0, 0
			var outputs []string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				n := requests
				requests++
				if inputs, ok := body["input"].([]any); ok {
					for _, input := range inputs {
						item, _ := input.(map[string]any)
						if item["type"] == "function_call_output" || item["type"] == "custom_tool_call_output" {
							raw, _ := json.Marshal(item)
							outputs = append(outputs, string(raw))
						}
					}
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(value any) { raw, _ := json.Marshal(value); fmt.Fprintf(w, "data: %s\n\n", raw) }
				send(map[string]any{"type": "response.created", "response": map[string]any{"id": "hosted-native-boundary"}})
				switch n {
				case 0:
					if native == "apply_patch" {
						send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "custom_tool_call", "call_id": "native-call", "name": native, "input": "*** Begin Patch\n*** Add File: native-change.txt\n+SHOULD_NOT_EXIST\n*** End Patch"}})
					} else {
						args := `{"agent_type":"default","message":"Reply READY; local deterministic fixture."}`
						if native == "exec_command" {
							args = `{"cmd":"printf SHOULD_NOT_EXIST > native-change.txt"}`
						}
						send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": "native-call", "name": native, "arguments": args}})
					}
				case 1:
					send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": "ledger-read", "name": "repos_list", "arguments": "{}"}})
				default:
					send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "ready", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "READY"}}}})
				}
				send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "hosted-native-boundary", "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
			}))
			defer backend.Close()
			config := fmt.Sprintf("permission_mode=\"bypass_permissions\"\nmodel_provider=\"local_probe\"\n[model_providers.local_probe]\nname=\"Local deterministic fixture\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\n", backend.URL+"/v1")
			if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := ThreadConfig(cwd, "probe-model")
			cfg.Config = maps.Clone(cfg.Config)
			cfg.Config["harness_mode"] = "codex"
			cfg.Config["model_harness"] = map[string]string{cfg.Model: "codex"}
			cfg.Config["model_tool_modes"] = map[string]string{cfg.Model: "direct"}
			cfg.Tools = []harness.ToolSpec{{Name: "repos_list", Description: "Read selected ledger repositories", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
			cfg.OnToolCall = func(_ context.Context, call harness.ToolCall) harness.ToolResult {
				if call.Tool != "repos_list" {
					t.Errorf("unexpected dynamic tool %s", call.Tool)
				}
				mu.Lock()
				dynamic++
				mu.Unlock()
				return harness.ToolResult{Success: true, Text: `{"fixture":"TEND_DYNAMIC_OK"}`}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var id string
			for _, phase := range []string{"start", "resume"} {
				mu.Lock()
				requests, dynamic = 0, 0
				outputs = nil
				mu.Unlock()
				session, err := traex.Start(ctx, traex.Options{Bin: bin, Dir: cwd, Env: []string{"TRAE_HOME=" + home, "TEND_ADDON=off"}, HostedCoordination: true})
				if err != nil {
					t.Fatal(err)
				}
				func() {
					defer session.Close()
					var thread harness.Thread
					if id == "" {
						thread, err = session.StartThread(ctx, cfg)
					} else {
						thread, err = session.ResumeThread(ctx, id, cfg)
					}
					if err != nil {
						t.Fatal(err)
					}
					id = thread.ID()
					turn, err := thread.StartTurn(ctx, harness.TurnInput{Text: "Check local execution boundary and read selected Tend ledger"})
					if err != nil {
						t.Fatal(err)
					}
					done := false
					for !done {
						select {
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						case event, ok := <-thread.Events():
							if !ok {
								t.Fatal("thread closed")
							}
							if event.Kind == harness.EventTurnCompleted && event.TurnID == turn {
								if event.TurnStatus != "completed" {
									t.Fatalf("turn status %s", event.TurnStatus)
								}
								done = true
							}
						}
					}
					mu.Lock()
					count, calls := requests, dynamic
					joined := strings.Join(outputs, "\n")
					mu.Unlock()
					t.Logf("%s %s local requests=%d dynamic=%d output=%s", native, phase, count, calls, joined)
					if count != 3 || calls != 1 || !strings.Contains(joined, "TEND_DYNAMIC_OK") || strings.Contains(joined, `\"agent_id\"`) {
						t.Fatalf("native execution or dynamic boundary failed: requests=%d dynamic=%d", count, calls)
					}
					switch native {
					case "spawn_agent", "send_input", "resume_agent", "wait_agent", "close_agent":
						if !strings.Contains(joined, "Tool call blocked by PreToolUse hook:") {
							t.Fatal("native delegation was not blocked")
						}
					case "exec_command":
						if !strings.Contains(joined, "unsupported call: exec_command") {
							t.Fatal("command was not rejected")
						}
					case "apply_patch":
						if !strings.Contains(joined, "read-only sandbox") {
							t.Fatal("patch was not rejected")
						}
					}
					if _, err := os.Stat(filepath.Join(cwd, "native-change.txt")); !os.IsNotExist(err) {
						t.Fatalf("native path wrote a file: %v", err)
					}
				}()
			}
			actual, err := os.ReadFile(filepath.Join(home, "traecli.toml"))
			if err != nil || string(actual) != config {
				t.Fatal("hosted adapter rewrote owner configuration", err)
			}
		})
	}
}
