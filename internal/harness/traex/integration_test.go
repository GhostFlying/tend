//go:build integration

package traex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func model() string {
	if m := os.Getenv("TEND_TRAEX_MODEL"); m != "" {
		return m
	}
	return "GPT-5.6-Sol"
}

// turn runs one turn and returns its agent text and final status.
func turn(t *testing.T, th harness.Thread, text string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id, err := th.StartTurn(ctx, harness.TurnInput{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		select {
		case <-ctx.Done():
			t.Fatal("turn timed out")
		case ev, ok := <-th.Events():
			if !ok {
				t.Fatal("events closed")
			}
			if ev.TurnID != id {
				continue
			}
			if ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemAgentMessage {
				out.WriteString(ev.Text)
			}
			if ev.Kind == harness.EventTurnCompleted {
				return out.String(), ev.TurnStatus
			}
		}
	}
}

// TestIntegration_SessionInjectToolsResume drives a real app-server: inject
// a developer message, call a dynamic tool, then resume the thread in a new
// process and check whether the tool is still registered.
func TestIntegration_SessionInjectToolsResume(t *testing.T) {
	ctx := context.Background()
	calls := make(chan harness.ToolCall, 8)
	cfg := harness.ThreadConfig{
		Cwd: t.TempDir(), Model: model(), Sandbox: "read-only", ApprovalPolicy: "never",
		DeveloperInstructions: "You are a test agent. Follow instructions literally and keep answers minimal.",
		Tools: []harness.ToolSpec{{Name: "tend_echo", Description: "Echo a message back. Use when asked to call tend_echo.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`)}},
		OnToolCall: func(_ context.Context, c harness.ToolCall) harness.ToolResult {
			calls <- c
			return harness.ToolResult{Success: true, Text: "ECHO " + string(c.Arguments)}
		},
	}
	s, err := Start(ctx, Options{Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := th.InjectDeveloper(ctx, "[tend context packet it1] The project codeword is HELIOTROPE."); err != nil {
		t.Fatal(err)
	}
	if text, status := turn(t, th, "What is the project codeword? Reply with only the codeword."); status != "completed" || !strings.Contains(text, "HELIOTROPE") {
		t.Errorf("codeword turn = %q (%s)", text, status)
	}
	turn(t, th, `Call the tend_echo tool with message "one", then reply with the tool output.`)
	select {
	case c := <-calls:
		if c.ThreadID != th.ID() || c.TurnID == "" || c.CallID == "" {
			t.Errorf("tool call identity = %+v", c)
		}
	default:
		t.Fatal("the tool was not called")
	}
	threadID := th.ID()
	if err := s.Close(); err != nil {
		t.Logf("close: %v", err)
	}

	s2, err := Start(ctx, Options{Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	th2, err := s2.ResumeThread(ctx, threadID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := turn(t, th2, `Call the tend_echo tool with message "two", then reply with the tool output. If you cannot, reply NO TOOL.`)
	select {
	case c := <-calls:
		t.Logf("after resume the tool is still registered: call %s", c.CallID)
	default:
		t.Logf("after resume the tool was not called; reply: %q", text)
	}
}
