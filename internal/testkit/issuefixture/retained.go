// Package issuefixture constructs explicitly retained hosted responsibilities for
// tests of preexisting receipt, tool isolation and recovery contracts. New Issue
// admission tests must use the owner API's backlog -> todo operations instead.
package issuefixture

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Retained appends a fixture definition and, when requested, retains the actual
// original source in the old hosted delivery format. It does not rewrite the
// create request or any historical definition.
func Retained(ctx context.Context, st *store.Store, ids protocol.IDGen, now time.Time, id, source string) (protocol.Issue, error) {
	var issue protocol.Issue
	err := st.Write(ctx, func(tx *store.Tx) error {
		var err error
		issue, err = store.GetCanonicalIssue(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if issue.WorkStatus != protocol.IssueBacklog {
			return nil
		}
		previous := issue.Revision
		issue.WorkStatus = protocol.IssueTodo
		issue.TodoAt = now.UnixMilli()
		issue.TodoRevision = previous + 1
		issue.Revision++
		request := ids.New(protocol.PrefixRequest)
		if err := tx.SaveCanonicalIssue(ctx, issue, previous, store.IssueRevisionProvenance{AdmissionDefinition: true, Actor: protocol.Principal{Kind: protocol.PrincipalSystem, Subject: "retained-test-fixture"}, RequestID: request, RequestDomain: "system", Reason: map[string]string{"reason": "Retained admitted responsibility fixture"}}); err != nil {
			return err
		}
		if issue.Assignee.Kind != protocol.IssueAssigneeAgent {
			return nil
		}
		// A promotion may already have a pending source group; reuse its durable
		// identity instead of constructing a second group for the same Issue.
		var existingBatch string
		if err := tx.Q().QueryRowContext(ctx, `SELECT batch_id FROM background_batches WHERE group_key=? AND status='pending'`, "work_item:"+id).Scan(&existingBatch); err == nil {
			return nil
		}
		conversation := "issue:" + id
		batch := ids.New(protocol.Prefix("batch"))
		message := ids.New(protocol.PrefixMessage)
		event := ids.New(protocol.PrefixEvent)
		seq, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: message, Author: "system", Body: "Retained hosted responsibility", CreatedAt: now.UnixMilli()})
		if err != nil {
			return err
		}
		if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: message, NoticeType: "background_batch"}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO background_batches(batch_id,group_key,work_item_id,message_id,status,created_at) VALUES(?,?,?,?,'pending',?)`, batch, "work_item:"+id, id, message, now.UnixMilli()); err != nil {
			return err
		}
		if err := tx.BindMessage(ctx, message, "work_item", id, "coordination"); err != nil {
			return err
		}
		if err := tx.BindMessage(ctx, message, "background_batch", batch, "coordination"); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","background_batch":true}`, RequestID: request, CreatedAt: now.UnixMilli()}); err != nil {
			return err
		}
		payload, _ := json.Marshal(app.HandleMessage{MessageID: message, EventID: event, EventKind: "background.batch"})
		if err := tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: ids.New(protocol.PrefixOutbox), RequestID: ids.New(protocol.PrefixRequest), ParentRequestID: request, Destination: store.ConversationDestination(conversation), Operation: app.OpHandleEvent, PayloadJSON: string(payload), CreatedAt: now.UnixMilli()}); err != nil {
			return err
		}
		if source != "" {
			var originalEvent string
			if err := tx.Q().QueryRowContext(ctx, `SELECT event_id FROM events WHERE object_type='message' AND object_id=? ORDER BY event_seq DESC LIMIT 1`, source).Scan(&originalEvent); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO background_sources(batch_id,message_id,event_id,kind,data_json) VALUES(?,?,?,'issue.delegated','{}')`, batch, source, originalEvent); err != nil {
				return err
			}
			return tx.BindMessage(ctx, source, "background_batch", batch, "source")
		}
		return nil
	})
	return issue, err
}
