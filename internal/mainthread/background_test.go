package mainthread

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBackgroundCheckinUsesHarnessIdentityAndNeverPublishesReply(t *testing.T) {
	h := testkit.NewHarness()
	results := make(chan harness.ToolResult, 2)
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		read := th.CallTool(context.Background(), turn, "read-sources", "background_sources", map[string]any{})
		results <- read
		if !read.Success {
			return "failed"
		}
		th.UseTool(turn, "source-audit", harness.ToolUse{Name: "background_sources", Output: read.Text, Status: protocol.ToolCompleted})
		th.Say(turn, "reply-before-checkin", "No new result; continue waiting")
		check := th.CallTool(context.Background(), turn, "record-check", "background_checkin", map[string]any{"note": "Waiting for the existing owner decision"})
		results <- check
		if !check.Success {
			return "failed"
		}
		return "completed"
	}
	e := newEnv(t, h)
	openWakeDecision(e)
	eventually(t, "background turn ends", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND status IN ('completed','failed')`) == 1
	})
	for range 2 {
		if result := <-results; !result.Success {
			t.Fatal(result.Text)
		}
	}
	if len(assistantItems(t, e)) != 0 {
		t.Fatal("silent check flashed ordinary output")
	}
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE kind='tool_use'`) != 1 {
		t.Fatal("silent check lost the tool audit")
	}
	if e.count(`SELECT COUNT(*) FROM decisions WHERE status='open'`) != 1 {
		t.Fatal("silent check answered owner decision")
	}
	if e.count(`SELECT COUNT(*) FROM background_batches WHERE status='completed' AND checkin_json<>''`) != 1 {
		t.Fatal("silent receipt not retained")
	}
}
