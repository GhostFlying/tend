//go:build integration

package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
)

// Explicit binaries are required. Every model response comes from loopback;
// provider credentials and personal native homes are never used.
func TestIntegration_LocalNativeUsage(t *testing.T) {
	for _, provider := range []string{"codex", "traex"} {
		t.Run(provider, func(t *testing.T) {
			variable := "TEND_NATIVE_CODEX_BIN"
			if provider == "traex" {
				variable = "TEND_NATIVE_TRAEX_BIN"
			}
			binary := os.Getenv(variable)
			if binary == "" {
				t.Skip("set " + variable + " to test the real CLI against a local model")
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				item := map[string]any{"id": fmt.Sprintf("message-%d", n), "type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "READY"}}}
				events := []any{
					map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("response-%d", n)}},
					map[string]any{"type": "response.output_item.done", "item": item},
					map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", n), "usage": map[string]any{"input_tokens": 100 * n, "input_tokens_details": map[string]int64{"cached_tokens": 20 * n}, "output_tokens": 10 * n, "output_tokens_details": map[string]int64{"reasoning_tokens": 2 * n}, "total_tokens": 110 * n}}},
				}
				for _, event := range events {
					raw, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}
			}))
			defer server.Close()
			home := t.TempDir()
			config := fmt.Sprintf(`permission_mode="bypass_permissions"
model_provider="fixture"
model="gpt-5.1-codex-mini"
[model_providers.fixture]
name="Local usage fixture"
base_url="%s/v1"
wire_api="responses"
requires_openai_auth=false
request_max_retries=0
stream_max_retries=0
supports_websockets=false
[features]
enable_request_compression=false
memories=false
plugins=false
apps=false
shell_tool=false
code_mode=false
js_repl=false
hooks=false
`, server.URL)
			for _, name := range []string{"config.toml", "traecli.toml"} {
				if err := os.WriteFile(filepath.Join(home, name), []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			options := Options{Bin: binary, Dir: home, Version: "native-usage-local-test", Env: []string{"CODEX_HOME=" + home, "TRAE_HOME=" + home, "OPENAI_API_KEY=", "CODEX_API_KEY=", "TEND_ADDON=off"}}
			open := func() *Session {
				s, err := Start(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				return s
			}
			cfg := harness.ThreadConfig{Cwd: home, Model: "gpt-5.1-codex-mini", Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "Reply READY. Do not use tools."}
			complete := func(th harness.Thread, id string) {
				for {
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case event := <-th.Events():
						if event.RequestUsage != nil {
							t.Fatal("durable measurement depends on experimental raw events")
						}
						if event.Kind == harness.EventTurnCompleted && (id == "" || event.TurnID == id) {
							return
						}
					}
				}
			}
			run := func(th harness.Thread) {
				id, err := th.StartTurn(ctx, harness.TurnInput{Text: "Reply READY. Do not use tools."})
				if err != nil {
					t.Fatal(err)
				}
				complete(th, id)
			}
			total := func(th harness.Thread) int64 {
				source := th.(harness.NativeUsageThread).UsageSource()
				page, err := nativeusage.Read(ctx, source, nativeusage.Cursor{}, 100)
				if err != nil {
					t.Fatal(err)
				}
				var sum int64
				for _, sample := range page.Samples {
					if sample.Provider != provider || sample.NativeVersion == "" || sample.SessionID == "" || sample.Model == "" {
						t.Fatalf("missing native metadata: %+v", sample)
					}
					sum += *sample.Total
				}
				return sum
			}
			first := open()
			th, err := first.StartThread(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			run(th)
			if err = first.Close(); err != nil {
				t.Fatal(err)
			}
			if got := total(th); got != 110 {
				t.Fatal(got)
			}
			second := open()
			resumed, err := second.ResumeThread(ctx, th.ID(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			run(resumed)
			if err = resumed.(harness.ContextManager).Compact(ctx); err != nil {
				t.Fatal(err)
			}
			complete(resumed, "")
			fork, err := second.ForkThread(ctx, resumed.ID(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			run(fork)
			if err = second.Close(); err != nil {
				t.Fatal(err)
			}
			if got := total(resumed); got != 660 {
				t.Fatalf("resume and compaction: got %d", got)
			}
			if got := total(fork); got != 440 {
				t.Fatalf("fork inherited cumulative consumption: got %d", got)
			}
			if calls.Load() != 4 {
				t.Fatal(calls.Load())
			}
		})
	}
}
