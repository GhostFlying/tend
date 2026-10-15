//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

func traexModel() string {
	if m := os.Getenv("TEND_TRAEX_MODEL"); m != "" {
		return m
	}
	return "GPT-5.6-Sol"
}

// gitIdentity isolates git from user configuration and gives commits an
// author.
func gitIdentity(t *testing.T) []string {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{"GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com"}
}

func git(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), env...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// calcRemote creates a bare repository holding a tiny Go module.
func calcRemote(t *testing.T, env []string) string {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	git(t, env, dir, "init", "--quiet", "--initial-branch=main", seed)
	files := map[string]string{
		"go.mod":       "module calc\n\ngo 1.22\n",
		"calc.go":      "package calc\n\n// Add returns a + b.\nfunc Add(a, b int) int { return a + b }\n",
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, env, seed, "add", ".")
	git(t, env, seed, "commit", "--quiet", "-m", "calc")
	url := filepath.Join(dir, "calc.git")
	git(t, env, dir, "clone", "--quiet", "--bare", seed, url)
	return url
}

func runWorker(t *testing.T, ws *traex.Workers, spec harness.WorkerSpec) harness.WorkerResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	w, err := ws.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for range w.Events() {
		events++
	}
	res, err := w.Wait()
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	t.Logf("%s worker: %d events; summary: %s", spec.Role, events, res.Summary)
	return res
}

// TestIntegration_TraexWorkers implements a small change with a real
// worker in a git workspace and has an independent acceptance worker
// judge it.
func TestIntegration_TraexWorkers(t *testing.T) {
	env := gitIdentity(t)
	url := calcRemote(t, env)
	spaces := gitws.New(gitws.Config{Home: t.TempDir(), Env: env})
	workers := traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Version: "it"}, Model: traexModel()})
	spec := json.RawMessage(`{"goal":"Add a function Sub(a, b int) int to calc.go in the calc repository that returns a - b, with a unit test.",` +
		`"acceptance":["go test ./... passes in the calc directory","Sub exists in calc.go and returns a - b"],"delivery":"commit"}`)
	impl := protocol.Assignment{RunID: "run_impl", TaskID: "task_1", Role: protocol.RoleImplement, Generation: 1, Spec: spec,
		Workspace: protocol.AssignmentWorkspace{Name: "add-sub", Branch: "tend/add-sub", Mounts: []protocol.AssignedMount{{Repo: "calc", Path: "calc", RemoteURL: url}}}}

	p, err := spaces.Prepare(context.Background(), impl)
	if err != nil {
		t.Fatal(err)
	}
	runWorker(t, workers, harness.WorkerSpec{RunID: impl.RunID, Role: impl.Role, Dir: p.Dir, ScratchDir: p.ScratchDir, Spec: spec, Attempt: 1})
	artifact, err := spaces.Collect(context.Background(), impl)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(p.Dir, "calc", "calc.go"))
	if err != nil || !strings.Contains(string(body), "func Sub") {
		t.Fatalf("calc.go after the worker:\n%s", body)
	}

	raw, _ := json.Marshal(artifact)
	acc := impl
	acc.RunID, acc.Role, acc.Artifact = "run_acc", protocol.RoleAccept, raw
	ap, err := spaces.Prepare(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	defer spaces.Finish(context.Background(), acc)
	res := runWorker(t, workers, harness.WorkerSpec{RunID: acc.RunID, Role: acc.Role, Dir: ap.Dir, ScratchDir: ap.ScratchDir, Spec: spec, Artifact: raw, Attempt: 1})
	if res.Verdict == nil {
		t.Fatalf("the acceptance worker gave no verdict; summary: %s", res.Summary)
	}
	t.Logf("verdict: %s %s", res.Verdict.Verdict, res.Verdict.Evidence)
	if res.Verdict.Verdict != protocol.VerdictPassed {
		t.Errorf("verdict = %s, want passed for a correct change", res.Verdict.Verdict)
	}
}
