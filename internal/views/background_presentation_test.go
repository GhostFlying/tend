package views

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBackgroundConversationRetainsRecipientAndBoundedOriginalSources(t *testing.T) {
	ctx := t.Context()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{"batch-message", "pending-message"} {
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: "generic background notification"}); err != nil {
				return err
			}
			if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: id, NoticeType: "background_batch"}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO background_batches(batch_id,group_key,work_item_id,message_id,status,created_at) VALUES(?,?,'',?,'pending',1)`, id, id, id); err != nil {
				return err
			}
		}
		for i := range 5 {
			id := fmt.Sprintf("source-%d", i)
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: "Task completed: " + strings.Repeat("完成", 100)}); err != nil {
				return err
			}
			if err := tx.BindMessage(ctx, id, "task", "original-task", "source"); err != nil {
				return err
			}
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: "event-" + id, Source: "app", ObjectType: "task", ObjectID: "original-task", ObjectVersion: 1, Kind: "task.completed", BodyJSON: "{}", CreatedAt: 1}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO background_sources(batch_id,message_id,event_id,kind,data_json,cancelled_at) VALUES('batch-message',?,?,'task.completed','{}',?)`, id, "event-"+id, i/4); err != nil {
				return err
			}
		}
		// Frozen selection, not the latest configuration or current assignee,
		// identifies the Agent selected for this exact original message.
		if _, err := tx.Exec(ctx, `INSERT INTO hosted_configuration_versions(revision,environment_id,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at) VALUES(1,'fixture','old','{}','{}','old-agent','req_old',1),(2,'fixture','new','{}','{}','new-agent','req_new',2)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO local_hosted_executions(execution_id,conversation_id,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES('original','main','main',1,'fixture/old-agent','{"identity":{"execution_environment_id":"fixture","local_id":"old-agent"},"definition":{"display_name":"原负责人"}}',1),('rejected','main','main',1,'fixture/other','{"identity":{"execution_environment_id":"fixture","local_id":"other"},"definition":{"display_name":"拒绝接收者"}}',0)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO main_turns(request_id,message_id,thread_id,status,created_at,updated_at) VALUES('original','batch-message','original-thread','completed',1,2),('rejected','batch-message','old-thread','rejected',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		page, err := Conversation(ctx, tx.Q(), 0, 0, 50)
		if err != nil {
			return err
		}
		items, err := OwnerIssueMessages(ctx, tx.Q(), page.Items)
		if err != nil {
			return err
		}
		var batch, pending *protocol.BackgroundPresentation
		for _, item := range items {
			if item.MessageID == "batch-message" {
				batch = item.Background.Presentation
			}
			if item.MessageID == "pending-message" {
				pending = item.Background.Presentation
			}
		}
		if batch == nil || batch.LastSourceSeq != 5 || batch.Recipient == nil || batch.Recipient.DisplayName != "原负责人" || batch.Recipient.Identity.LocalID != "old-agent" || batch.Recipient.Status != "completed" {
			t.Fatalf("frozen recipient = %+v", batch)
		}
		if len(batch.Sources) != 3 || !batch.More || batch.Sources[0].MessageID != "source-0" || batch.Sources[0].Kind != "task.completed" || batch.Sources[0].ObjectType != "task" || batch.Sources[0].ObjectID != "original-task" || len([]rune(batch.Sources[0].Summary)) != 161 {
			t.Fatalf("bounded active sources = %+v", batch.Sources)
		}
		if pending == nil || pending.Recipient != nil || len(pending.Sources) != 0 {
			t.Fatalf("pending recipient invented = %+v", pending)
		}
		originals, err := store.BackgroundSources(ctx, tx.Q(), "batch-message", 0)
		if err != nil {
			return err
		}
		if len(originals.Sources) != 5 || originals.Sources[4].CancelledAt != 1 || !strings.HasSuffix(originals.Sources[0].Body, strings.Repeat("完成", 100)) {
			t.Fatalf("originals were changed by the preview = %+v", originals)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conversation_messages SET author='main_agent',body='{"tend:retained-json:v2":9999999}' WHERE message_id='source-0'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		batch, err := store.BackgroundByMessage(ctx, tx.Q(), "batch-message")
		if err != nil {
			return err
		}
		presentation, err := backgroundPresentation(ctx, tx.Q(), batch)
		if err != nil || len(presentation.Sources) != 3 || presentation.Sources[0].Summary != "原始来源暂时无法读取；展开后重试。" || presentation.Sources[1].Summary == "" {
			t.Fatalf("one unreadable original blocked unrelated previews: %+v, %v", presentation, err)
		}
		if _, err := store.BackgroundSources(ctx, tx.Q(), "batch-message", 0); err == nil {
			t.Fatal("optional preview converted missing original into a successful source read")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
