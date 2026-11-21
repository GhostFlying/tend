package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func issueNotice(e Event) bool {
	switch e.Kind {
	case "work_item.closed", "work_item.reopened":
		return true
	}
	return false
}

func recordIssueNotice(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
	var canonical bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, e.ObjectID).Scan(&canonical); err != nil {
		return err
	}
	if canonical {
		// The same committed event already enters the shared owner-change
		// window. Retain this immediate notice path only for legacy work items.
		return nil
	}
	item, err := store.GetIssue(ctx, tx.Q(), e.ObjectID)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("工作项：%s（%s）", item.WorkItemID, item.Title)
	switch e.Kind {
	case "work_item.closed":
		label := map[string]string{"completed": "已完成", "declined": "决定不做", "handed_off": "已交接", "superseded": "被替代"}[string(item.Closure.Kind)]
		body += fmt.Sprintf("\n\n已结束跟进 · %s\n%s", label, item.Closure.Reason)
	case "work_item.reopened":
		body += "\n\n已重新打开，历史执行与验收记录保持不变。"
	}
	now := x.Now.UnixMilli()
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, CreatedAt: now})
	if err != nil {
		return err
	}
	if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: id, NoticeType: "work_item_updated"}); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, id, "work_item", item.WorkItemID, "notice"); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, id, "event", eventID, "notice"); err != nil {
		return err
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"system"}`, CreatedAt: now})
	if err != nil {
		return err
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		return nil
	}
	return queueEventTurn(ctx, x, tx, id, eventID, e.Kind, nil)
}
