package orca

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeCLI answers orca commands with canned replies and records them.
type fakeCLI struct {
	calls   [][]string
	dirs    []string
	replies map[string]string // by "<group> <command>"
	err     error
}

func (f *fakeCLI) exec(_ context.Context, dir string, args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	f.dirs = append(f.dirs, dir)
	return []byte(f.replies[args[0]+" "+args[1]]), f.err
}

func newFake(replies map[string]string) (*Client, *fakeCLI) {
	f := &fakeCLI{replies: replies}
	return &Client{Exec: f.exec}, f
}

func TestCreateWorktree(t *testing.T) {
	c, f := newFake(map[string]string{"worktree create": `{"id":"r1","ok":true,"result":{"worktree":{
		"id":"repo-1::/w/task-1","repoId":"repo-1","path":"/w/task-1","head":"7b10d15","branch":"refs/heads/me/task-1",
		"displayName":"task-1","baseRef":"main","isBare":false}},"_meta":{"runtimeId":"x"}}`})
	w, err := c.CreateWorktree(context.Background(), CreateWorktree{Repo: "id:repo-1", Name: "task-1", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Path != "/w/task-1" || w.Head != "7b10d15" || w.BranchName() != "me/task-1" || w.Selector() != "id:repo-1::/w/task-1" {
		t.Errorf("worktree = %+v", w)
	}
	want := []string{"worktree", "create", "--repo", "id:repo-1", "--name", "task-1", "--no-parent", "--setup", "skip", "--base-branch", "main", "--json"}
	if !slices.Equal(f.calls[0], want) {
		t.Errorf("args = %q, want %q", f.calls[0], want)
	}
}

func TestLaunchAgentReplay(t *testing.T) {
	c, f := newFake(map[string]string{"agent launch": `{"id":"r","ok":false,"error":{"code":"agent_session_operation_unknown",
		"message":"Orca could not confirm the launch outcome","data":{"launchOutcome":"unknown","retrySafe":true,"operationId":"op-7"}}}`})
	o := LaunchOptions{Dir: "/w/task-1", Agent: "codex", Worktree: "id:repo-1::/w/task-1", Prompt: "do it", Model: "gpt", Effort: "high"}
	_, err := c.LaunchAgent(context.Background(), o)
	if got := LaunchRetryID(err); got != "op-7" {
		t.Fatalf("LaunchRetryID(%v) = %q", err, got)
	}
	f.replies["agent launch"] = `{"id":"r","ok":true,"result":{"outcome":{"kind":"terminal","handle":"term_1","paneKey":"p"},
		"worktreeId":"repo-1::/w/task-1","receipt":{"mode":"terminal","detail":"Started a terminal agent."},
		"prompt":{"delivery":"submit","outcome":"handed-to-terminal"},"operationId":"op-7"}}`
	o.RetryLaunch = LaunchRetryID(err)
	l, err := c.LaunchAgent(context.Background(), o)
	if err != nil || l.Outcome.Kind != OutcomeTerminal || l.Outcome.Handle != "term_1" || l.OperationID != "op-7" || l.Prompt.Outcome != "handed-to-terminal" {
		t.Fatalf("launch = %+v, %v", l, err)
	}
	want := []string{"agent", "launch", "--agent", "codex", "--worktree", "id:repo-1::/w/task-1", "--prompt", "do it",
		"--model", "gpt", "--effort", "high", "--retry-launch", "op-7", "--json"}
	if !slices.Equal(f.calls[1], want) || f.dirs[1] != "/w/task-1" {
		t.Errorf("replay args = %q in %q, want %q", f.calls[1], f.dirs[1], want)
	}
}

func TestSendInput(t *testing.T) {
	c, f := newFake(map[string]string{"terminal send": `{"id":"r","ok":true,"result":{"send":{"handle":"term_1","accepted":true,
		"bytesWritten":61,"prompt":{"requestId":"req-1","stages":["input_accepted","turn_started"],"provider":"codex",
		"baselineWorkingSequence":3}},"mutation":{"requestId":"req-1","replayed":false}}}`})
	s, err := c.SendInput(context.Background(), SendOptions{Terminal: "term_1", Text: "go on", Enter: true, WaitSubmit: 10 * time.Second})
	if err != nil || !s.Accepted || s.Prompt == nil || s.Prompt.RequestID != "req-1" || !slices.Contains(s.Prompt.Stages, StageTurnStarted) ||
		s.Prompt.BaselineWorkingSequence != 3 {
		t.Fatalf("send = %+v, %v", s, err)
	}
	want := []string{"terminal", "send", "--terminal", "term_1", "--text", "go on", "--enter", "--wait-submit", "10", "--json"}
	if !slices.Equal(f.calls[0], want) {
		t.Errorf("args = %q, want %q", f.calls[0], want)
	}
	f.replies["terminal send"] = `{"ok":false,"error":{"code":"runtime_unavailable","message":"lost. Orchestration mutation request ID: req-2.",
		"data":{"orchestrationRequestId":"req-2","originalCommand":"terminal send"}}}`
	if _, err := c.SendInput(context.Background(), SendOptions{Terminal: "term_1", Text: "x", Enter: true}); SendRetryID(err) != "req-2" {
		t.Errorf("SendRetryID(%v) = %q", err, SendRetryID(err))
	}
}

func TestWaitFor(t *testing.T) {
	c, f := newFake(map[string]string{"terminal wait": `{"id":"r","ok":true,"result":{"wait":{"handle":"term_1","condition":"tui-idle",
		"satisfied":false,"status":"running","exitCode":null,"blockedReason":"agent-trust-workspace"}}}`})
	w, err := c.WaitFor(context.Background(), "term_1", WaitTUIIdle, 90*time.Second)
	if err != nil || w.Satisfied || w.Status != "running" || w.BlockedReason != "agent-trust-workspace" || w.ExitCode != nil {
		t.Fatalf("wait = %+v, %v", w, err)
	}
	if got := strings.Join(f.calls[0], " "); got != "terminal wait --terminal term_1 --for tui-idle --timeout-ms 90000 --json" {
		t.Errorf("args = %s", got)
	}
}

func TestAgentsOfOneWorktree(t *testing.T) {
	c, _ := newFake(map[string]string{"worktree ps": `{"ok":true,"result":{"worktrees":[
		{"worktreeId":"other","agents":[{"paneKey":"a","state":"working"}]},
		{"worktreeId":"repo-1::/w/task-1","agents":[{"paneKey":"p","state":"done","agentType":"codex","prompt":"do it",
			"lastAssistantMessage":"done it","interrupted":false,"stateStartedAt":5,"updatedAt":6}]}],"truncated":false}}`})
	agents, err := c.Agents(context.Background(), "repo-1::/w/task-1")
	if err != nil || len(agents) != 1 || agents[0].State != "done" || agents[0].Prompt != "do it" || agents[0].LastAssistantMessage != "done it" {
		t.Fatalf("agents = %+v, %v", agents, err)
	}
	if agents, err := c.Agents(context.Background(), "missing"); err != nil || agents != nil {
		t.Errorf("agents of a missing worktree = %+v, %v", agents, err)
	}
}

func TestFailures(t *testing.T) {
	c, f := newFake(map[string]string{"worktree list": `{"ok":false,"error":{"code":"repo_not_found","message":"repo_not_found"}}`})
	_, err := c.Worktrees(context.Background(), "id:nope")
	if e, ok := errors.AsType[*Error](err); !ok || e.Code != "repo_not_found" {
		t.Errorf("error = %v, want repo_not_found", err)
	}
	if LaunchRetryID(err) != "" {
		t.Error("a failure without an operation offers a replay")
	}
	f.replies["worktree list"] = "orca: command not found"
	f.err = errors.New("exit status 127")
	if _, err := c.Worktrees(context.Background(), "id:nope"); err == nil || !strings.Contains(err.Error(), "exit status 127") {
		t.Errorf("error = %v, want the run failure", err)
	}
	f.replies["worktree list"] = `{"ok":true,"result":{"worktrees":[],"truncated":true}}`
	f.err = nil
	if _, err := c.Worktrees(context.Background(), "id:big"); err == nil {
		t.Error("a truncated listing passed for complete")
	}
}
