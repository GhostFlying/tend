package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runWorker prepares a scratch git repository with a task worktree and lets a
// workspace-write thread implement a small change there, the way a runner
// would drive an implementation run.
func (s *spike) runWorker(ctx context.Context, root string) error {
	repo := filepath.Join(root, "repo")
	wt := filepath.Join(root, "wt-task")
	if err := setupScratchRepo(ctx, repo, wt); err != nil {
		return err
	}
	if err := s.start(ctx); err != nil {
		return err
	}
	defer s.c.close()

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		InstructionSources []string `json:"instructionSources"`
	}
	err := s.c.call(ctx, "thread/start", map[string]any{
		"model":          s.model,
		"cwd":            wt,
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
		"config": map[string]any{
			"features.plugins":            false,
			"features.apps":               false,
			"features.memories":           false,
			"skills.bundled.enabled":      false,
			"skills.include_instructions": false,
		},
		"developerInstructions": "You are a tend implementation worker. Change only files in the current repository. Do not run git commands that create commits or branches; the runner commits your work.",
	}, &started)
	if err != nil {
		return fmt.Errorf("thread/start: %w", err)
	}
	threadID := started.Thread.ID
	s.results["worker_thread"] = map[string]any{"threadId": threadID, "instructionSources": started.InstructionSources}

	s.ask(ctx, threadID, "worker_implement",
		"Add a function `Sub(a, b int) int` to calc.go with a table-driven test in calc_test.go, run `go test ./...`, and report the test result in one line.", nil)

	// Probes for what the sandbox allows inside a worktree.
	gitDir := gitOutput(ctx, wt, "rev-parse", "--git-dir")
	s.ask(ctx, threadID, "worker_probe_git",
		"Run exactly these two commands and report each exit code in one line: `touch "+filepath.Join(gitDir, "tend-probe")+"` and `curl -sS -m 5 -o /dev/null https://example.com`.", nil)
	_, gitWriteErr := os.Stat(filepath.Join(gitDir, "tend-probe"))
	s.results["worker_git_dir_writable"] = gitWriteErr == nil

	// The runner, not the model, turns the working tree into an artifact.
	s.results["worker_status"] = gitOutput(ctx, wt, "status", "--porcelain")
	s.results["worker_test"] = runIn(ctx, wt, "go", "test", "./...")
	if out := runIn(ctx, wt, "git", "add", "-A"); out != "" {
		s.results["worker_add"] = out
	}
	s.results["worker_commit"] = runIn(ctx, wt, "git", "-c", "user.name=tend-runner", "-c", "user.email=runner@tend.invalid", "commit", "-q", "-m", "task: implement Sub")
	s.results["worker_artifact"] = gitOutput(ctx, wt, "log", "-1", "--format=%H %s")
	s.results["worker_diffstat"] = gitOutput(ctx, wt, "diff", "--stat", "HEAD~1", "HEAD")
	return nil
}

func setupScratchRepo(ctx context.Context, repo, wt string) error {
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"go.mod":  "module example.com/calc\n\ngo 1.27\n",
		"calc.go": "package calc\n\n// Add returns a + b.\nfunc Add(a, b int) int { return a + b }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=tend-spike", "-c", "user.email=spike@tend.invalid", "commit", "-q", "-m", "init"},
		{"worktree", "add", "-q", "-b", "task/sub", wt},
	}
	for _, args := range steps {
		if out := runIn(ctx, repo, "git", args...); strings.Contains(out, "fatal") {
			return fmt.Errorf("git %v: %s", args, out)
		}
	}
	return nil
}

func runIn(ctx context.Context, dir, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil {
		res = strings.TrimSpace(res + " (" + err.Error() + ")")
	}
	return res
}

func gitOutput(ctx context.Context, dir string, args ...string) string {
	return runIn(ctx, dir, "git", args...)
}
