//go:build integration

package traex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// summarizeTurns logs the turns and dynamic tool call items of a raw
// thread/resume or thread/read response.
func summarizeTurns(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var res struct {
		Thread struct {
			Status json.RawMessage `json:"status"`
			Turns  []struct {
				ID     string            `json:"id"`
				Status string            `json:"status"`
				Items  []json.RawMessage `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Logf("undecodable response: %v", err)
		return
	}
	t.Logf("thread status %s, %d turns", res.Thread.Status, len(res.Thread.Turns))
	for _, turn := range res.Thread.Turns {
		t.Logf("turn %s status=%s items=%d", turn.ID, turn.Status, len(turn.Items))
		for _, it := range turn.Items {
			var item struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			}
			_ = json.Unmarshal(it, &item)
			if item.Type == "dynamicToolCall" {
				t.Logf("  dynamicToolCall: %s", it)
			} else {
				t.Logf("  %s %s", item.Type, item.Status)
			}
		}
	}
}

// TestIntegration_ToolCallCrash kills the app-server while a dynamic tool
// call waits for tend's answer, resumes the thread in a new process and
// records what became of the call: lost, sent again or hanging (plan §8.5).
func TestIntegration_ToolCallCrash(t *testing.T) {
	ctx := context.Background()
	spec := harness.ToolSpec{Name: "tend_slow", Description: "Record a message. Use when asked to call tend_slow.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`)}
	first := make(chan harness.ToolCall, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	cfg := harness.ThreadConfig{
		Cwd: t.TempDir(), Model: model(), Sandbox: "read-only", ApprovalPolicy: "never",
		DeveloperInstructions: "You are a test agent. Follow instructions literally and keep answers minimal.",
		Tools:                 []harness.ToolSpec{spec},
		OnToolCall: func(ctx context.Context, c harness.ToolCall) harness.ToolResult {
			first <- c
			select {
			case <-ctx.Done():
			case <-release:
			}
			return harness.ToolResult{Text: "too late"}
		},
	}
	s1, err := Start(ctx, Options{Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s1.Close() })
	th, err := s1.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := th.StartTurn(ctx, harness.TurnInput{Text: `Call the tend_slow tool with message "one", then reply with exactly what it returned.`})
	if err != nil {
		t.Fatal(err)
	}
	var orig harness.ToolCall
	select {
	case orig = <-first:
		t.Logf("call %s in turn %s is waiting; killing the app-server", orig.CallID, turnID)
	case <-time.After(5 * time.Minute):
		t.Fatal("the tool was not called")
	}
	_ = s1.Close()

	s2, err := Start(ctx, Options{Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	later := make(chan harness.ToolCall, 8)
	onTool := func(_ context.Context, c harness.ToolCall) harness.ToolResult {
		later <- c
		return harness.ToolResult{Success: true, Text: "RECORDED " + string(c.Arguments)}
	}
	th2 := s2.register(th.ID(), onTool)
	var raw json.RawMessage
	if err := s2.conn.call(ctx, methodThreadResume, ThreadResumeParams{
		ThreadID: th.ID(), Model: cfg.Model, Cwd: cfg.Cwd, ApprovalPolicy: cfg.ApprovalPolicy, Sandbox: cfg.Sandbox,
		DeveloperInstructions: cfg.DeveloperInstructions,
	}, &raw); err != nil {
		t.Fatal(err)
	}
	summarizeTurns(t, raw)

	// Anything the resumed process does on its own: events of the old
	// turn, or the call delivered again.
	quiet := time.After(90 * time.Second)
watch:
	for {
		select {
		case ev, ok := <-th2.Events():
			if !ok {
				t.Fatal("the resumed app-server ended")
			}
			t.Logf("event after resume: %s turn=%s item=%s status=%s", ev.Kind, ev.TurnID, ev.ItemType, ev.TurnStatus)
		case c := <-later:
			t.Logf("call delivered again after resume: call=%s turn=%s same_id=%v", c.CallID, c.TurnID, c.CallID == orig.CallID)
		case <-quiet:
			break watch
		}
	}

	text, status := turn(t, th2, `Did your previous call to tend_slow return anything? Answer in one sentence, then call tend_slow with message "two" and reply with its output.`)
	t.Logf("next turn %s: %q", status, text)
	for len(later) > 0 {
		c := <-later
		t.Logf("call in the next turn: call=%s turn=%s args=%s", c.CallID, c.TurnID, c.Arguments)
	}
	if err := s2.conn.call(ctx, "thread/read", map[string]any{"threadId": th.ID(), "includeTurns": true}, &raw); err != nil {
		t.Logf("thread/read: %v", err)
	} else {
		summarizeTurns(t, raw)
	}
}

// TestIntegration_WorkspaceInstructions records which AGENTS.md files a
// thread loads when its cwd is a workspace root that is not a repository,
// with and without runtimeWorkspaceRoots (D37).
func TestIntegration_WorkspaceInstructions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "calc")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "AGENTS.md"), "The workspace codeword is AMBER.\n")
	write(filepath.Join(repo, "AGENTS.md"), "The repository codeword is COBALT.\n")

	s, err := Start(ctx, Options{Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, roots := range [][]string{nil, {root, repo}} {
		params := map[string]any{
			"model": model(), "cwd": root, "approvalPolicy": "never", "sandbox": "read-only",
			"developerInstructions": "You are a test agent. Answer from your instructions only; do not run commands or read files.",
		}
		if roots != nil {
			params["runtimeWorkspaceRoots"] = roots
		}
		var raw json.RawMessage
		if err := s.conn.call(ctx, methodThreadStart, params, &raw); err != nil {
			t.Logf("roots=%v: thread/start: %v", roots, err)
			continue
		}
		var res struct {
			InstructionSources []string `json:"instructionSources"`
			Thread             struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(raw, &res)
		t.Logf("roots=%v: instructionSources=%v", roots, res.InstructionSources)
		th := s.register(res.Thread.ID, nil)
		text, status := turn(t, th, "List every codeword your instructions mention, or reply NONE.")
		t.Logf("roots=%v: %s %q", roots, status, text)
	}
}
