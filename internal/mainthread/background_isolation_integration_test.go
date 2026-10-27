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
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
)

// Check the actual native model request, rather than assuming the harness
// recognizes a configuration key. The model endpoint is local and deterministic.
func TestIntegrationBackgroundAutoMode(t *testing.T) { probeBackgroundToolIsolation(t, false) }

// This opt-in diagnostic retains the original strict-isolation experiment.
// Its expected failure is not an auto-mode enablement gate. No real owner
// messages or files are submitted to the local deterministic model endpoint.
func TestIntegrationNativeBackgroundAllowlistProbe(t *testing.T) {
	if os.Getenv("TEND_PROBE_NATIVE_TOOL_ALLOWLIST") != "1" {
		t.Skip("diagnostic probe requires explicit opt-in")
	}
	probeBackgroundToolIsolation(t, true)
}
func probeBackgroundToolIsolation(t *testing.T, diagnostic bool) {
	bin, err := exec.LookPath("traex")
	if err != nil {
		t.Skip("traex not installed")
	}
	var mu sync.Mutex
	var requests []map[string]any
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			data, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		send(map[string]any{"type": "response.created", "response": map[string]any{"id": "isolation-probe"}})
		if !diagnostic && n%2 == 1 {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": fmt.Sprintf("read-%d", n), "name": "background_item_read", "arguments": "{}"}})
		} else {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "reply", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "READY"}}}})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "isolation-probe", "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer backend.Close()
	home, cwd := t.TempDir(), t.TempDir()
	config := fmt.Sprintf("model_provider = \"dispatcher_probe\"\n[model_providers.dispatcher_probe]\nname = \"dispatcher probe\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n", backend.URL+"/v1")
	config = `permission_mode = "bypass_permissions"
` + config
	config += "\n[mcp_servers.must_not_start]\ncommand = \"/nonexistent/tend-probe-command\"\n"
	if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg := ThreadConfig(cwd, "probe-model")
	if !diagnostic {
		cfg = backgroundThreadConfig(cfg)
	}
	if diagnostic {
		cfg.Config = maps.Clone(cfg.Config)
		cfg.Config["harness_mode"] = "codex"
		cfg.Config["model_harness"] = map[string]string{cfg.Model: "codex"}
		cfg.Config["model_tool_modes"] = map[string]string{cfg.Model: "direct"}
		cfg.Config["mcp_servers.must_not_start.enabled"] = false
		cfg.Config["tools.view_image"] = false
	}
	toolName := "background_item_read"
	if diagnostic {
		toolName = "repos_list"
	}
	cfg.Config["mcp_servers.must_not_start.enabled"] = false
	cfg.Tools = []harness.ToolSpec{{Name: toolName, Description: "Read the assigned work item", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
	var calls int
	cfg.OnToolCall = func(_ context.Context, c harness.ToolCall) harness.ToolResult {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if c.Tool != toolName {
			t.Errorf("unexpected dynamic tool %s", c.Tool)
		}
		return harness.ToolResult{Success: true, Text: `{"work_item_id":"fixture","revision":1}`}
	}
	start := func() *traex.Session {
		s, err := traex.Start(ctx, traex.Options{Bin: bin, Dir: cwd, Env: []string{"TRAE_HOME=" + home, "TEND_ADDON=off"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	s := start()
	th, err := s.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	run := func(th harness.Thread) {
		if !diagnostic {
			actual := th.(harness.PermissionedThread).PermissionDefaults()
			want := harness.PermissionSettings{Sandbox: "workspace-write", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review"}
			if actual != want {
				t.Fatalf("native permissions = %+v, want %+v", actual, want)
			}
			t.Logf("native permissions: %+v", actual)
		}
		turn, err := th.StartTurn(ctx, harness.TurnInput{Text: "Reply READY"})
		if err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case ev, ok := <-th.Events():
				if !ok {
					t.Fatal("thread closed")
				}
				if ev.Kind == harness.EventTurnCompleted && ev.TurnID == turn {
					if ev.TurnStatus != "completed" {
						t.Fatalf("turn status %s", ev.TurnStatus)
					}
					return
				}
			}
		}
	}
	run(th)
	id := th.ID()
	_ = s.Close()
	s = start()
	th, err = s.ResumeThread(ctx, id, cfg)
	if err != nil {
		t.Fatal(err)
	}
	run(th)
	mu.Lock()
	defer mu.Unlock()
	if !diagnostic && (len(requests) != 4 || calls != 2) {
		t.Fatalf("requests=%d dynamic calls=%d; start and resume must both complete tool calls", len(requests), calls)
	}
	if diagnostic && len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, request := range requests {
		foundRepos := false
		toolList, _ := request["tools"].([]any)
		for _, raw := range toolList {
			tool, _ := raw.(map[string]any)
			name, _ := tool["name"].(string)
			if name == toolName {
				foundRepos = true
			}
			if diagnostic && name != toolName {
				t.Errorf("background request exposes unexpected tool %s", name)
			}
			if !diagnostic {
				continue
			}
			switch name {
			case "exec_command", "shell", "shell_command", "write_stdin", "apply_patch", "js_repl", "spawn_agent":
				t.Errorf("request %d exposes execution tool %s", i, name)
			}
			if tool["type"] == "local_shell" || tool["type"] == "web_search" || tool["type"] == "web_search_preview" {
				t.Errorf("request %d exposes native research/execution tool %v", i, tool)
			}
		}
		if !foundRepos {
			t.Errorf("request %d missing background read tool", i)
		}
	}
}
