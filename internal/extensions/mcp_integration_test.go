//go:build linux && integration

package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIntegrationManagedMCPRespectsCoordinatorRole(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("native Codex unavailable")
	}
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Version string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(q.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch q.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": q.Params.Version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "managed-fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "fixture_echo", "description": "Controlled local MCP tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
	}))
	defer mcp.Close()
	var mu sync.Mutex
	requests := [][]byte{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid model request")
			return
		}
		tools, _ := json.Marshal(body["tools"])
		mu.Lock()
		requests = append(requests, tools)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []any{map[string]any{"type": "response.created", "response": map[string]any{"id": "mcp-fixture"}}, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "reply", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "READY"}}}}, map[string]any{"type": "response.completed", "response": map[string]any{"id": "mcp-fixture", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			raw, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer model.Close()
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	base := filepath.Join(root, "base.toml")
	if err = os.WriteFile(base, []byte(fmt.Sprintf("model_provider=\"fixture\"\n[model_providers.fixture]\nname=\"Local fixture\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\n", model.URL+"/v1")), 0600); err != nil {
		t.Fatal(err)
	}
	resource := protocol.ExtensionResource{Kind: "mcp", ID: "fixture.server", Version: "1", MCP: &protocol.ExtensionMCP{Transport: "http", URL: mcp.URL, Required: true, EnabledTools: []string{"fixture_echo"}}}
	r, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{resource}, Categories: []protocol.ExtensionBinding{{CategoryID: "planner", Choices: []protocol.ExtensionChoice{{Kind: "mcp", ID: "fixture.server", Version: "1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := Consumer{UserHome: user, Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "home"), BaseConfig: base}}}}
	if err = c.Receive(t.Context(), r, nil); err != nil {
		t.Fatal(err)
	}
	combo, err := Resolve(r, "planner", protocol.AgentIdentity{ExecutionEnvironmentID: "fixture", LocalID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := c.Prepare(t.Context(), combo.Pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	for _, coordinator := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
		cwd := t.TempDir()
		id := "run:mcp-fixture"
		if coordinator {
			id = "coordinator:mcp-fixture"
		}
		var nativeErrors bytes.Buffer
		s, err := traex.Start(ctx, traex.Options{Stderr: &nativeErrors, Bin: "codex", Dir: cwd, Environment: binding, EnvironmentExecution: id, HostedCoordination: coordinator})
		if err != nil {
			cancel()
			t.Fatalf("coordinator=%t: %v; native stderr: %s", coordinator, err, nativeErrors.String())
		}
		cfg := harness.ThreadConfig{Cwd: cwd, Model: "fixture-model", Sandbox: "read-only", ApprovalPolicy: "never", NetworkAccess: true}
		if coordinator {
			cfg.Config = coordination.Isolation()
			cfg.Tools = []harness.ToolSpec{{Name: "tend_fixture_read", Description: "Controlled Tend bridge", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}
		}
		thread, err := s.StartThread(ctx, cfg)
		if err == nil {
			_, err = thread.StartTurn(ctx, harness.TurnInput{Text: "Reply READY without tools."})
		}
		if err != nil {
			_ = s.Close()
			cancel()
			t.Fatal(err)
		}
		finished := false
		for !finished {
			select {
			case <-ctx.Done():
				_ = s.Close()
				cancel()
				t.Fatal("native MCP turn timed out")
			case event, ok := <-thread.Events():
				if !ok {
					_ = s.Close()
					cancel()
					t.Fatal("native event stream ended")
				}
				if event.Kind == harness.EventTurnCompleted {
					finished = true
				}
			}
		}
		if err = s.Close(); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		status, readErr := (&codexenv.Service{Root: binding.Directory}).Read(binding.ID)
		if readErr != nil || len(status.Adoptions[id].MCP) == 0 {
			t.Fatal("native MCP observations were not retained with the adopted combination", readErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model requests=%d", len(requests))
	}
	if !bytes.Contains(requests[0], []byte("fixture_echo")) {
		t.Fatalf("worker MCP tool absent from actual request: %s", requests[0])
	}
	if bytes.Contains(requests[1], []byte("fixture_echo")) {
		t.Fatal("managed MCP reopened a coordinator capability")
	}
	if !bytes.Contains(requests[1], []byte("tend_fixture_read")) {
		t.Fatal("coordinator lost its scoped Tend bridge")
	}
}
