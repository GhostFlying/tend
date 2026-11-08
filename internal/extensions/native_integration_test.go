//go:build linux && integration

package extensions

import (
	"bytes"
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

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIntegrationManagedViewUsesNativeDiscovery(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("native Codex unavailable")
	}
	p := fixturePackage(t)
	agent := protocol.AgentIdentity{ExecutionEnvironmentID: "integration", LocalID: "worker"}
	r, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{p.Resource}, Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Revision: 1, Choices: []protocol.ExtensionChoice{{Kind: "skill", ID: p.Resource.ID, Version: p.Resource.Version}}, AgentsMD: "MANAGED_CATEGORY_INSTRUCTIONS"}}, Agents: []protocol.ExtensionBinding{{Agent: agent, Revision: 1, AgentsMD: "MANAGED_AGENT_INSTRUCTIONS"}}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c := Consumer{UserHome: user, Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "home"), AuthSource: filepath.Join(user, ".codex", "auth.json")}}}}
	if err = c.Receive(t.Context(), r, func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(p.Content)), nil
	}); err != nil {
		t.Fatal(err)
	}
	combo, err := Resolve(r, "implementer", agent)
	if err != nil {
		t.Fatal(err)
	}
	binding, status, err := c.Prepare(t.Context(), combo.Pin, "native")
	if err != nil || !status.Ready {
		t.Fatal("native preparation", status, err)
	}
	ref, err := binding.Pin(t.Context(), "run:integration")
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	skillDir := filepath.Join(project, ".agents", "skills", "project-canary")
	if err = os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: project-canary\ndescription: Project discovery fixture\n---\nPROJECT_SKILL_BODY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("PROJECT_INSTRUCTIONS_BODY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Project and user files remain physically visible. Native skill rules
	// control personal discovery; owned capabilities live in this process home.
	cmd, cleanup, err := ref.Command(t.Context(), "/bin/sh", []string{"-c", `test -f .agents/skills/project-canary/SKILL.md && test -f AGENTS.md && test -f "$CODEX_HOME/skills/"*/SKILL.md && grep -q MANAGED_CATEGORY_INSTRUCTIONS "$CODEX_HOME/AGENTS.md" && grep -q MANAGED_AGENT_INSTRUCTIONS "$CODEX_HOME/AGENTS.md"`}, project, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	procx.Contain(cmd)
	out, err := cmd.CombinedOutput()
	procx.KillGroup(cmd)
	if err != nil {
		t.Fatalf("filesystem view: %v %s", err, out)
	}
	if model := os.Getenv("TEND_CODEX_ENV_MODEL"); model != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cmd, cleanup, err := ref.Command(ctx, "codex", []string{"exec", "--skip-git-repo-check", "--model", model, "--json", "Use $project-canary and $fixture. Reply exactly MANAGED_ENVIRONMENT_OK. Do not call tools."}, project, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		procx.Contain(cmd)
		out, err := cmd.CombinedOutput()
		procx.KillGroup(cmd)
		if err != nil {
			t.Fatalf("minimal managed model turn failed: %v", err)
		}
		if !strings.Contains(string(out), "MANAGED_ENVIRONMENT_OK") {
			t.Fatal("model turn did not complete")
		}
		t.Log("native model turn and fixed session home verified")
	}
}

func TestIntegrationNativeRequestsKeepProjectsAndThrottlePersonalExclusions(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("native Codex unavailable")
	}
	user := t.TempDir()
	t.Setenv("HOME", user)
	writeSkill := func(root, name, body string) {
		t.Helper()
		dir := filepath.Join(root, ".agents", "skills", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Native request fixture\n---\n"+body+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(user, "ambient-canary", "KNOWN_PERSONAL_BODY_MUST_BE_ABSENT")
	project := t.TempDir()
	writeSkill(project, "project-canary", "PROJECT_BODY_MUST_BE_PRESENT")
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("PROJECT_AGENTS_MUST_BE_PRESENT"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := [][]byte{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []any{map[string]any{"type": "response.created", "response": map[string]any{"id": "managed-fixture"}}, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "reply", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "MANAGED_FIXTURE_OK"}}}}, map[string]any{"type": "response.completed", "response": map[string]any{"id": "managed-fixture", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			raw, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer backend.Close()
	root := t.TempDir()
	base := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("model_provider=\"managed_fixture\"\n[model_providers.managed_fixture]\nname=\"Local fixture\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\n", backend.URL+"/v1")
	if err := os.WriteFile(base, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	p := fixturePackage(t)
	identity := protocol.AgentIdentity{ExecutionEnvironmentID: "fixture", LocalID: "worker"}
	r, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{p.Resource}, Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Choices: []protocol.ExtensionChoice{{Kind: "skill", ID: p.Resource.ID, Version: p.Resource.Version}}, AgentsMD: "CATEGORY_BODY_MUST_BE_FIRST"}}, Agents: []protocol.ExtensionBinding{{Agent: identity, AgentsMD: "AGENT_BODY_MUST_BE_SECOND"}}})
	if err != nil {
		t.Fatal(err)
	}
	c := Consumer{UserHome: user, Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "state"), BaseConfig: base}}}}
	if err = c.Receive(t.Context(), r, func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(p.Content)), nil
	}); err != nil {
		t.Fatal(err)
	}
	combo, err := Resolve(r, "implementer", identity)
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := c.Prepare(t.Context(), combo.Pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := binding.Pin(t.Context(), "run:request-fixture")
	if err != nil {
		t.Fatal(err)
	}
	firstThread := ""
	for attempt := range 4 {
		if attempt == 1 {
			writeSkill(user, "late-personal-canary", "LATE_PERSONAL_BODY_MUST_BE_ABSENT")
		}
		if attempt == 2 {
			// Expire only this fixture's persisted cache to exercise the next
			// launch refresh without a six-hour wait or a production force flag.
			path := filepath.Join(user, ".cache", "tend", "codex-skill-exclusions", "inventory.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cache map[string]any
			if err = json.Unmarshal(raw, &cache); err != nil {
				t.Fatal(err)
			}
			cache["last_attempt"] = time.Now().Add(-7 * time.Hour).UTC().Format(time.RFC3339Nano)
			raw, _ = json.Marshal(cache)
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		args := []string{"exec", "--skip-git-repo-check", "--model", "fixture-model", "--sandbox", "read-only", "--json"}
		if attempt == 3 {
			args = append(args, "resume", firstThread)
		}
		args = append(args, "Use $ambient-canary, $late-personal-canary, $project-canary and $fixture. Reply MANAGED_FIXTURE_OK without tools.")
		cmd, cleanup, err := ref.Command(ctx, "codex", args, project, os.Environ())
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		procx.Contain(cmd)
		output, err := cmd.CombinedOutput()
		procx.KillGroup(cmd)
		cleanup()
		cancel()
		if err != nil {
			t.Fatalf("native fixture turn %d: %v %s", attempt, err, output)
		}
		for _, line := range bytes.Split(output, []byte("\n")) {
			var event struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
			}
			if json.Unmarshal(line, &event) == nil && event.Type == "thread.started" {
				if attempt == 0 {
					firstThread = event.ThreadID
				} else if attempt == 3 && event.ThreadID != firstThread {
					t.Fatal("native resume changed the original thread")
				}
			}
		}
		if firstThread == "" {
			t.Fatal("native fixture did not report its thread identity")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("native request count: %d", len(requests))
	}
	for attempt, raw := range requests {
		for _, marker := range []string{"PROJECT_BODY_MUST_BE_PRESENT", "PROJECT_AGENTS_MUST_BE_PRESENT", "CATEGORY_BODY_MUST_BE_FIRST", "AGENT_BODY_MUST_BE_SECOND", "managed body"} {
			if !bytes.Contains(raw, []byte(marker)) {
				t.Errorf("missing native request marker %s", marker)
			}
		}
		for _, marker := range []string{"KNOWN_PERSONAL_BODY_MUST_BE_ABSENT"} {
			if bytes.Contains(raw, []byte(marker)) {
				t.Errorf("ambient skill expanded: %s", marker)
			}
		}
		if bytes.Contains(raw, []byte("LATE_PERSONAL_BODY_MUST_BE_ABSENT")) != (attempt == 1) {
			t.Error("native request did not respect the six-hour exclusion snapshot", attempt)
		}
		if bytes.Index(raw, []byte("CATEGORY_BODY_MUST_BE_FIRST")) > bytes.Index(raw, []byte("AGENT_BODY_MUST_BE_SECOND")) {
			t.Error("native AGENTS ordering changed")
		}
	}
}
