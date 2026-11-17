package mainthread

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
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

func TestBackgroundNativePublicToolVisibleWhileTurnStillRunning(t *testing.T) {
	h := testkit.NewHarness()
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.UseTool(turn, "shell", harness.ToolUse{Name: "exec_command", Input: `{"cmd":"pwd"}`, Status: protocol.ToolRunning})
		<-release
		th.UseTool(turn, "shell", harness.ToolUse{Name: "exec_command", Input: `{"cmd":"pwd"}`, Output: "public result", Status: protocol.ToolCompleted})
		return "completed"
	}
	e := newEnv(t, h)
	openWakeDecision(e)
	eventually(t, "public native tool projects before frozen turn ends", func() bool {
		var visible bool
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var message string
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT message_id FROM background_batches WHERE status='frozen' LIMIT 1`).Scan(&message); err != nil {
				return nil
			}
			old, err := views.TranscriptBlock(t.Context(), tx.Q(), message, 0, 50)
			if err != nil {
				return err
			}
			block, err := views.OwnerIssueTranscriptBlock(t.Context(), tx.Q(), old)
			if err != nil {
				return err
			}
			visible = len(block.Items) == 0 && block.Process != nil && len(block.Process.Items) == 1 && block.Process.Items[0].Tool.Name == "exec_command" && block.Process.Items[0].Tool.Status == protocol.ToolRunning
			if visible {
				var pending int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM refresh_notifications WHERE kind='conversation.process_changed' AND object_id=? AND pending=1`, message).Scan(&pending); err != nil {
					return err
				}
				visible = pending == 1
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return visible
	})
}
