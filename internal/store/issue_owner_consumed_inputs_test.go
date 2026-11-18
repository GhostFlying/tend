package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueOwnerConsumedInputMigrationPreservesHistoricalReceipt(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "schema131.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	t.Cleanup(func() { s.Close() })
	if err := s.migrate(ctx, 131); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000001")
	saveCanonicalFixture(t, s, issue, 0)
	conversation := "issue:" + issue.IssueID
	if err := s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,work_item_id,created_at) VALUES(?,?,1) ON CONFLICT(work_item_id) DO NOTHING`, conversation, issue.IssueID); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, TaskRow{TaskID: "task_old", Title: "Original owner work", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, ActionRow{ActionID: "act_old", TaskID: "task_old", Kind: protocol.ActionImplement, EffectKey: "old", TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: 1, Status: protocol.ActionDone, CreatedRequestID: "req_old"}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, RunRow{RunID: "run_old", TaskID: "task_old", ActionID: "act_old", Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished}); err != nil {
			return err
		}
		for _, id := range []string{"old-original", "ordinary"} {
			if _, err := tx.InsertMessage(ctx, MessageRow{MessageID: id, ConversationID: conversation, Author: "owner", Body: id}); err != nil {
				return err
			}
			if err := tx.InsertOutbox(ctx, OutboxRow{OutboxID: "outbox-" + id, RequestID: "request-" + id, Destination: ConversationDestination(conversation), Operation: "main_thread.handle_message", PayloadJSON: `{"message_id":"` + id + `"}`}); err != nil {
				return err
			}
		}
		if _, err := tx.InsertMessage(ctx, MessageRow{MessageID: "old-notice", ConversationID: conversation, Author: "external", Body: "Original urgent notice"}); err != nil {
			return err
		}
		if err := tx.InsertIssueOwnerSteer(ctx, IssueOwnerSteer{IssueOwnerSteerView: protocol.IssueOwnerSteerView{
			MessageID: "old-notice", IssueID: issue.IssueID, SourceMessageID: "old-original", SourceRevision: 1,
			RunID: "run_old", TaskID: "task_old", ThreadID: "old-thread", TurnID: "old-turn", Sender: protocol.Principal{Kind: protocol.PrincipalOwner},
		}}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE issue_owner_steers SET status='consumed', native_item_id='old-native-item' WHERE message_id='old-notice'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, 132); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		q := tx.Q()
		blocks, err := TranscriptBlocks(ctx, q, 0, 10, conversation)
		if err != nil || len(blocks) != 1 || blocks[0].ID != "old-original" {
			t.Fatal(blocks, err)
		}
		receipt, err := ConsumedIssueOwnerInput(ctx, q, "old-original")
		if err != nil || receipt.MessageID != "old-notice" || receipt.NativeItemID != "old-native-item" || receipt.Status != "consumed" {
			t.Fatal(receipt, err)
		}
		rows, total, err := ConversationQueue(ctx, q, "owner", 0, 10, conversation)
		if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != "ordinary" {
			t.Fatal(rows, total, err)
		}
		head, err := HeadOutboxPrioritized(ctx, q, ConversationDestination(conversation), "main_thread.handle_event")
		if err != nil || head.RequestID != "request-ordinary" {
			t.Fatal(head, err)
		}
		original, err := GetOutboxByRequest(ctx, q, "request-old-original")
		if err != nil || original.Status != OutboxPending || original.Attempts != 0 {
			t.Fatal(original, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
