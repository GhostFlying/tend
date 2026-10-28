//go:build integration

package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

// A real app-server, hosted Manager, dispatcher and write transaction. The local
// model endpoint retries a committed plan with a new call ID after a status
// steer. Real-model understanding is covered separately by the hosted test.
func TestIntegrationHostedSteerPlanningRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"title": "one approved task", "spec": spec, "commit_seq": 1})
	var modelCalls, opens atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		n := modelCalls.Add(1)
		input, _ := json.Marshal(body["input"])
		if n == 2 && !strings.Contains(string(input), "STATUS_ONLY_KEEP_GOAL") {
			t.Error("retry model request did not contain the actual status steer")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value any) {
			b, _ := json.Marshal(value)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		send(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint(n)}})
		if n <= 2 {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": fmt.Sprintf("plan-call-%d", n), "name": "tasks_create", "arguments": string(args)}})
		} else {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "final", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "STATUS_ONLY_KEEP_GOAL: one task remains recorded; no duplicate work."}}}})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint(n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	t.Cleanup(backend.Close)
	home, cwd := t.TempDir(), t.TempDir()
	config := fmt.Sprintf("model_provider = \"planning_steer_probe\"\n[model_providers.planning_steer_probe]\nname = \"planning steer probe\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	type observed struct {
		call   harness.ToolCall
		result harness.ToolResult
	}
	results := make(chan observed, 2)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var tools atomic.Int32
	e := newEnv(t, testkit.NewHarness(), func(cfg *Config) {
		cfg.Thread = ThreadConfig(cwd, "probe")
		cfg.Open = func(openCtx context.Context) (harness.Session, error) {
			s, err := traex.Start(openCtx, traex.Options{Dir: cwd, Env: []string{"TRAE_HOME=" + home, "TEND_ADDON=off"}, Log: quiet()})
			if err != nil {
				return nil, err
			}
			opens.Add(1)
			return nativeObservedSession{Session: s, onTool: func(toolCtx context.Context, c harness.ToolCall, h harness.ToolHandler) harness.ToolResult {
				result := h(toolCtx, c)
				n := tools.Add(1)
				results <- observed{c, result}
				if n == 1 {
					select {
					case <-release:
					case <-toolCtx.Done():
					}
				}
				return result
			}}, nil
		}
	})
	first := e.say("Record exactly one approved task and preserve KEEP_GOAL")
	getResult := func() observed {
		t.Helper()
		select {
		case got := <-results:
			return got
		case <-ctx.Done():
			t.Fatal("native planning callback missing: ", ctx.Err())
			return observed{}
		}
	}
	original := getResult()
	if !original.result.Success || e.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatal("initial plan did not commit: ", original.result.Text)
	}
	var message struct {
		ID string `json:"message_id"`
	}
	if err := json.Unmarshal(e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "STATUS_ONLY_KEEP_GOAL: give status only, no additional tasks are authorized", ReplyTo: first}, app.SubmitMessage{}), &message); err != nil {
		t.Fatal(err)
	}
	eventually(t, "native acceptance before original tool returned", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted'`, message.ID) == 1
	})
	if e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) != 0 || e.count(`SELECT COUNT(*) FROM main_steers WHERE native_item_id<>''`) != 0 {
		t.Fatal("original turn ended or steer consumption was claimed during held tool")
	}
	close(release)
	retry := getResult()
	if retry.call.CallID == original.call.CallID || retry.call.ThreadID != original.call.ThreadID || retry.call.TurnID != original.call.TurnID || !strings.Contains(retry.call.UserText, message.ID) {
		t.Fatalf("native retry lost same turn or current owner input: %+v", retry.call)
	}
	var duplicate struct {
		Error *protocol.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(retry.result.Text), &duplicate); err != nil || retry.result.Success || duplicate.Error == nil || duplicate.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatal("status steer repeated committed planning effect: ", retry.result.Text)
	}
	eventually(t, "same original turn completes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND thread_id=? AND turn_id=? AND status='completed'`, first, original.call.ThreadID, original.call.TurnID) == 1
	})
	if opens.Load() != 1 || tools.Load() != 2 || e.count(`SELECT COUNT(*) FROM tasks`) != 1 || e.count(`SELECT COUNT(*) FROM plan_commits WHERE source_message_id=?`, first) != 1 || e.count(`SELECT COUNT(*) FROM main_turns t JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE e.repair=0`) != 1 ||
		e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed' AND native_item_id<>''`, message.ID) != 1 ||
		e.count(`SELECT COUNT(*) FROM conversation_messages WHERE author='main_agent' AND reply_to=? AND body LIKE '%STATUS_ONLY_KEEP_GOAL%'`, message.ID) != 1 {
		t.Fatal("duplicate write/session/turn or missing native consumption and reply proof")
	}
	// This endpoint exercises planning retries, not the mandatory metadata
	// review. Its final reply must leave an unreviewed logical responsibility.
	rec, found, err := e.app.TurnDelivery(t.Context(), original.call.ThreadID, original.call.TurnID)
	if err != nil || !found {
		t.Fatal(rec, found, err)
	}
	state, err := e.app.LogicalTurn(t.Context(), rec.RequestID)
	if err != nil || state.Status == "completed" {
		t.Fatal("native completion bypassed formal review", state, err)
	}
	t.Logf("thread=%s turn=%s original=%s status=%s native_calls=%s,%s tools=2 tasks=1 sessions=1; retry=%s", original.call.ThreadID, original.call.TurnID, first, message.ID, original.call.CallID, retry.call.CallID, retry.result.Text)
}
