//go:build linux && integration

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
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
)

func TestIntegrationCodexEnvironmentAdoptionPreservesNativeThreadAndRollout(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("native Codex unavailable")
	}
	root := t.TempDir()
	user, _ := os.UserHomeDir()
	disabled := false
	svc := codexenv.Service{Root: filepath.Join(root, "store")}
	profile := codexenv.Profile{ID: "coordinator", Home: filepath.Join(root, "home"), UserHome: user, Enabled: true, BundledSkills: &disabled}
	// Exercise both metadata-only persistence and a real native history from
	// the deterministic loopback model, without account authentication.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []any{
			map[string]any{"type": "response.created", "response": map[string]string{"id": "fixture-response"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "fixture-message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "IDENTITY_OK"}}}},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "fixture-response", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			raw, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer backend.Close()
	profile.BaseConfig = filepath.Join(root, "base.toml")
	if err := os.WriteFile(profile.BaseConfig, []byte(fmt.Sprintf("model_provider='fixture'\n[model_providers.fixture]\nname='Fixture'\nbase_url=%q\nwire_api='responses'\nrequires_openai_auth=false\nsupports_websockets=false\n[features]\nenable_request_compression=false\n", backend.URL+"/v1")), 0600); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Bin: bin, Dir: root, Environment: &codexenv.Binding{Directory: svc.Root, ID: profile.ID}, EnvironmentExecution: "coordinator:identity"}
	session, err := Start(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	cfg := harness.ThreadConfig{Cwd: root, Model: "gpt-5.1-codex-mini", Sandbox: "read-only", ApprovalPolicy: "never"}
	original, err := session.StartThread(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if idle, err := session.EnvironmentQuiescent(t.Context(), original.ID()); err != nil || !idle {
		t.Fatal("metadata-only thread could not reach a durable idle boundary", err)
	}
	if output, status := turn(t, original, "Reply IDENTITY_OK without tools."); status != "completed" || output != "IDENTITY_OK" {
		t.Fatal("loopback model did not materialize completed native history", status)
	}
	path := original.(harness.ThreadLocation).RolloutPath()
	physicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(physicalPath, filepath.Join(profile.Home, "sessions")+string(filepath.Separator)) {
		t.Fatal("native rollout escaped the fixed usage collector root")
	}
	profile.BundledSkills = nil
	if _, err = svc.Execute(t.Context(), codexenv.Request{Action: "update", ID: profile.ID, RequestID: "update", ExpectedRevision: created.Status.Revision, Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	idle, err := session.EnvironmentQuiescent(t.Context(), original.ID())
	if err != nil || !idle {
		t.Fatal("native thread did not confirm the idle adoption boundary", err)
	}
	if err = session.AdoptEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := Start(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	resumed, err := replacement.ResumeThread(t.Context(), original.ID(), cfg)
	if err != nil {
		t.Fatal("resume failed", err)
	}
	if resumed.ID() != original.ID() || resumed.(harness.ThreadLocation).RolloutPath() != path {
		t.Fatalf("snapshot adoption changed native identity or usage path: before=%s %s after=%s %s", original.ID(), path, resumed.ID(), resumed.(harness.ThreadLocation).RolloutPath())
	}
}

func TestIntegrationCodexEnvironmentResumeFailureRestoresSnapshot(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("native Codex unavailable")
	}
	root := t.TempDir()
	user, _ := os.UserHomeDir()
	disabled := false
	svc := codexenv.Service{Root: filepath.Join(root, "store")}
	profile := codexenv.Profile{ID: "coordinator", Home: filepath.Join(root, "home"), UserHome: user, Enabled: true, BundledSkills: &disabled}
	created, err := svc.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	binding := &codexenv.Binding{Directory: svc.Root, ID: profile.ID}
	old, err := binding.Pin(t.Context(), "coordinator:resume-failure")
	if err != nil {
		t.Fatal(err)
	}
	profile.BundledSkills = nil
	if _, err = svc.Execute(t.Context(), codexenv.Request{Action: "update", ID: profile.ID, RequestID: "update", ExpectedRevision: created.Status.Revision, Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	if err = old.Adopt(t.Context()); err != nil {
		t.Fatal(err)
	}
	session, err := Start(t.Context(), Options{Bin: bin, Dir: root, Environment: binding, EnvironmentExecution: old.Execution})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err = session.ResumeThread(t.Context(), "missing-environment-thread", harness.ThreadConfig{Cwd: root, Sandbox: "read-only", ApprovalPolicy: "never"}); err == nil {
		t.Fatal("missing thread was resumed")
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	fallback, err := binding.Pin(context.Background(), old.Execution)
	if err != nil || fallback.Generation != old.Generation || !fallback.Current() {
		t.Fatal("thread attachment failure lost previous snapshot", err)
	}
}

func TestIntegrationCodexEnvironmentPreservesCoordinatorBoundary(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("native Codex unavailable")
	}
	root := t.TempDir()
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	svc := codexenv.Service{Root: filepath.Join(root, "store")}
	profile := codexenv.Profile{ID: "coordinator", Home: filepath.Join(root, "home"), UserHome: user, Enabled: true, BundledSkills: &disabled}
	if _, err = svc.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	session, err := Start(t.Context(), Options{Bin: bin, Dir: root, HostedCoordination: true, Environment: &codexenv.Binding{Directory: svc.Root, ID: profile.ID}, EnvironmentExecution: "integration:coordinator"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{Cwd: root, Sandbox: "read-only", ApprovalPolicy: "never", Config: coordination.Isolation(), Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	permissions, ok := thread.(harness.PermissionedThread)
	if !ok {
		t.Fatal("native permissions unavailable")
	}
	effective := permissions.PermissionDefaults()
	if effective.Sandbox != "read-only" || effective.ApprovalPolicy != "never" {
		t.Fatal("environment changed coordinator permissions")
	}
}
