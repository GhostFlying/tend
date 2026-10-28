//go:build integration

package tools

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
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
)

// Exercise native schema normalization, which can erase deep schemas when a
// large tool exceeds its compaction threshold. Field names in prose do not
// prove that the model actually receives typed input parameters.
func TestIntegrationNativePlanningParameterTypes(t *testing.T) {
	bin, err := exec.LookPath("traex")
	if err != nil {
		t.Skip("traex not installed")
	}
	requests := make(chan map[string]any, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
			return
		}
		select {
		case requests <- request:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []any{
			map[string]any{"type": "response.created", "response": map[string]any{"id": "schema-probe"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "schema-message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "schema-probe", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			raw, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer backend.Close()
	home, cwd := t.TempDir(), t.TempDir()
	config := fmt.Sprintf("model_provider = \"probe\"\n[model_providers.probe]\nname = \"probe\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	session, err := traex.Start(ctx, traex.Options{Bin: bin, Dir: cwd, Env: []string{"TRAE_HOME=" + home}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, mode := range []bool{false, true} {
		t.Run(fmt.Sprintf("requested_code_mode_%t", mode), func(t *testing.T) {
			th, err := session.StartThread(ctx, harness.ThreadConfig{Cwd: cwd, Model: "probe-model", Ephemeral: true, Tools: Specs(), Sandbox: "read-only", ApprovalPolicy: "never", Config: map[string]any{"features.code_mode": mode, "features.code_mode_only": mode}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := session.ReleaseThread(ctx, th.ID()); err != nil {
					t.Error(err)
				}
			}()
			if _, err := th.StartTurn(ctx, harness.TurnInput{Text: "Return done. This is an isolated schema transport probe."}); err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case event := <-th.Events():
					if event.Kind == harness.EventTurnCompleted {
						if event.TurnStatus != "completed" {
							t.Fatalf("probe turn failed: %+v", event)
						}
						goto completed
					}
				}
			}
		completed:
			var request map[string]any
			select {
			case request = <-requests:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			found := map[string]bool{}
			for _, raw := range request["tools"].([]any) {
				tool := raw.(map[string]any)
				name, _ := tool["name"].(string)
				var path []string
				switch name {
				case "tasks_create":
					path = []string{"spec", "inputs", "[]", "file_id"}
				case "work_items_create":
					path = []string{"initial_context", "source_reviews", "[]", "message_id"}
				default:
					continue
				}
				schema := tool["parameters"].(map[string]any)
				if field := planningSchemaAt(t, schema, path...); field["type"] != "string" {
					t.Fatalf("%s lost typed nested field: %+v", name, field)
				}
				found[name] = true
			}
			if len(found) != 2 {
				t.Fatalf("planning tools missing from native request: %+v", found)
			}
		})
	}
}
