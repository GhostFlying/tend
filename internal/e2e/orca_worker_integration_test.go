//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

// orcaWorker runs one worker to its end and returns its result and the
// thread it journaled.
func orcaWorker(t *testing.T, ws *orca.Workers, spec harness.WorkerSpec) (harness.WorkerResult, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var thread string
	spec.OnThread = func(s string) error { thread = s; return nil }
	w, err := ws.Start(ctx, spec)
	if err != nil {
		t.Fatalf("start the %s worker: %v", spec.Role, err)
	}
	for ev := range w.Events() {
		t.Logf("%s worker: %s %s", spec.Role, ev.Kind, ev.Body)
	}
	res, err := w.Wait()
	if err != nil {
		t.Fatalf("%s worker: %v", spec.Role, err)
	}
	return res, thread
}

// TestIntegration_OrcaWorkers implements a small change with an agent TUI
// in orca and has a second agent accept it through the runner's MCP
// bridge. It needs a running orca and a scratch repository registered in
// it: TEND_ORCA_REPO selects it (such as name:<repo>), and its origin is
// only fetched. TEND_ORCA_AGENT picks the agent (codex by default),
// TEND_ORCA_MODEL its model, TEND_ORCA_BIN the orca CLI. The worktree and
// terminals the test creates are removed afterwards.
func TestIntegration_OrcaWorkers(t *testing.T) {
	selector := os.Getenv("TEND_ORCA_REPO")
	if selector == "" {
		t.Skip("TEND_ORCA_REPO names no scratch repository in orca")
	}
	agent := protocol.AgentKind(os.Getenv("TEND_ORCA_AGENT"))
	if agent == "" {
		agent = protocol.AgentCodex
	}
	ctx := context.Background()
	oc := &orca.Client{Bin: os.Getenv("TEND_ORCA_BIN")}
	repo, err := oc.Repo(ctx, selector)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := exec.Command("git", "-C", repo.Path, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("the scratch repository needs an origin: %v", err)
	}
	// The agent reaches the bridge through the runner's own binary.
	bin := filepath.Join(t.TempDir(), "tend-runner")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/GhostFlying/tend/cmd/tend-runner").CombinedOutput(); err != nil {
		t.Fatalf("build tend-runner: %v\n%s", err, out)
	}

	// Bridge sockets of its own, so that runs of the test for other agents
	// keep theirs; short, for the length limit of socket paths.
	sockets, err := os.MkdirTemp("", "tw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })

	spaces := gitws.NewOrca(gitws.Config{Home: t.TempDir(), Repos: map[string]gitws.Repo{"scratch": {Orca: selector}}}, oc)
	workers := orca.NewWorkers(orca.WorkerOptions{
		Client: oc, Agent: agent, Model: os.Getenv("TEND_ORCA_MODEL"), BridgeCommand: bin, BridgeArgs: []string{"worker-mcp"},
		SocketDir: sockets, IDs: protocol.NewULIDGen(time.Now, nil), Now: time.Now,
	})
	name := fmt.Sprintf("tend-it-%d", time.Now().Unix())
	spec := json.RawMessage(`{"goal":"Add a file tend-it.txt at the root of the repository whose only line is: hello from tend",` +
		`"acceptance":["tend-it.txt exists at the root of the repository","its only line is: hello from tend"],"delivery":"commit"}`)
	impl := protocol.Assignment{RunID: "run_impl", TaskID: "task_1", Role: protocol.RoleImplement, Generation: 1, Spec: spec,
		Workspace: protocol.AssignmentWorkspace{Name: name, Mounts: []protocol.AssignedMount{{Repo: "scratch", Path: "scratch", RemoteURL: strings.TrimSpace(string(origin))}}}}

	p, err := spaces.Prepare(ctx, impl)
	if err != nil {
		t.Fatal(err)
	}
	threads := map[string]string{}
	t.Cleanup(func() {
		for run, th := range threads {
			if err := workers.Release(ctx, run, th); err != nil {
				t.Logf("close the agent: %v", err)
			}
		}
		if err := oc.RemoveWorktree(ctx, "path:"+p.Dir); err != nil {
			t.Logf("remove the worktree %s: %v", p.Dir, err)
		}
		if m := p.Report.Mounts; len(m) == 1 && m[0].Branch != "" {
			_ = exec.Command("git", "-C", repo.Path, "branch", "-D", m[0].Branch).Run()
		}
	})
	_, threads[impl.RunID] = orcaWorker(t, workers, harness.WorkerSpec{RunID: impl.RunID, Role: impl.Role, Dir: p.Dir, ScratchDir: p.ScratchDir, Guide: p.Guide, Spec: spec, Attempt: 1})
	artifact, err := spaces.Collect(ctx, impl)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(p.Dir, "tend-it.txt")); err != nil || strings.TrimSpace(string(body)) != "hello from tend" {
		t.Fatalf("tend-it.txt after the worker = %q, %v", body, err)
	}

	raw, _ := json.Marshal(artifact)
	acc := impl
	acc.RunID, acc.Role, acc.Artifact = "run_acc", protocol.RoleAccept, raw
	ap, err := spaces.Prepare(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	defer spaces.Finish(ctx, acc)
	var res harness.WorkerResult
	res, threads[acc.RunID] = orcaWorker(t, workers, harness.WorkerSpec{RunID: acc.RunID, Role: acc.Role, Dir: ap.Dir, ScratchDir: ap.ScratchDir, Guide: ap.Guide, Spec: spec, Artifact: raw, Attempt: 1})
	if res.Verdict == nil {
		t.Fatalf("the acceptance worker gave no verdict; summary: %s", res.Summary)
	}
	t.Logf("verdict: %s %s", res.Verdict.Verdict, res.Verdict.Evidence)
	if res.Verdict.Verdict != protocol.VerdictPassed {
		t.Errorf("verdict = %s, want passed for a correct change", res.Verdict.Verdict)
	}
}
