//go:build integration

package mainthread

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

// Real native permission resolution with a local deterministic model endpoint.
// No deployed credentials, owner messages or external model requests are used.
func TestIntegrationNativeNetworkPermissions(t *testing.T) {
	bin, err := exec.LookPath("traex")
	if err != nil {
		t.Skip("traex not installed")
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"permissions\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"reply\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"READY\"}]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"permissions\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n")
	}))
	defer backend.Close()
	for _, role := range []string{"main", "background", "worker"} {
		t.Run(role, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			config := fmt.Sprintf("model_provider = \"probe\"\n[model_providers.probe]\nname = \"probe\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n", backend.URL+"/v1")
			if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			s, err := traex.Start(ctx, traex.Options{Bin: bin, Dir: cwd, Env: []string{"TRAE_HOME=" + home, "TEND_ADDON=off"}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cfg := ThreadConfig(cwd, "probe-model")
			want := "read-only"
			if role == "background" {
				cfg = backgroundThreadConfig(cfg)
				cfg.Config["sandbox_workspace_write.exclude_slash_tmp"] = true
				cfg.Config["sandbox_workspace_write.exclude_tmpdir_env_var"] = true
				want = "workspace-write"
			}
			if role == "worker" {
				cfg = harness.ThreadConfig{Cwd: cwd, Model: "probe-model", Sandbox: "danger-full-access", ApprovalPolicy: "never"}
				want = "danger-full-access"
			}
			th, err := s.StartThread(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := th.StartTurn(ctx, harness.TurnInput{Text: "Reply READY"})
			if err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case ev := <-th.Events():
					if ev.Kind != harness.EventTurnCompleted || ev.TurnID != turn {
						continue
					}
					if ev.TurnStatus != "completed" {
						t.Fatalf("turn %s", ev.TurnStatus)
					}
					goto completed
				}
			}
		completed:
			path := th.(harness.ThreadLocation).RolloutPath()
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 4096), 8<<20)
			var policy struct {
				Type          string `json:"type"`
				Network       bool   `json:"network_access"`
				ExcludeTmp    bool   `json:"exclude_slash_tmp"`
				ExcludeEnvTmp bool   `json:"exclude_tmpdir_env_var"`
			}
			for scanner.Scan() {
				var d struct {
					Type    string `json:"type"`
					Payload struct {
						Policy json.RawMessage `json:"sandbox_policy"`
					} `json:"payload"`
				}
				if json.Unmarshal(scanner.Bytes(), &d) == nil && d.Type == "turn_context" {
					_ = json.Unmarshal(d.Payload.Policy, &policy)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if policy.Type != want || (role != "worker" && !policy.Network) {
				t.Fatalf("native policy %+v, want %s with networking", policy, want)
			}
			if role == "background" && (!policy.ExcludeTmp || !policy.ExcludeEnvTmp) {
				t.Fatalf("native filesystem exclusions lost: %+v", policy)
			}
			t.Logf("native %s: filesystem=%s network_enabled=%v", role, policy.Type, role == "worker" || policy.Network)
		})
	}
}
