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
	case "work_item.delivery_changed":
		if e.Body["refresh_pending"] == true {
			return false
		}
		old, _ := e.Body["previous_state"].(string)
		state, _ := e.Body["state"].(string)
		previousError, _ := e.Body["previous_error_kind"].(string)
		currentError, _ := e.Body["error_kind"].(string)
		return (state != old && (old != "" || state == "merged" || state == "closed")) || (previousError == "" && currentError != "") || (previousError != "" && currentError == "")
	}
	return false
}

func recordIssueNotice(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
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
	case "work_item.delivery_changed":
		body += fmt.Sprintf("\n\n交付 %v 状态更新：%v。请结合已有意图核对下一步；状态变化本身不授权合入或部署。", e.Body["delivery_id"], e.Body["state"])
		if kind, ok := e.Body["error_kind"].(string); ok && kind != "" {
			body += "\n最新查询失败，保留上次已核验状态：" + kind
		}
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
	if e.Kind == "work_item.delivery_changed" {
		if _, err := tx.Exec(ctx, `UPDATE trigger_events SET message_id=? WHERE id=(SELECT event_id FROM observation_jobs WHERE issue_id=? AND delivery_id=?)`, id, item.WorkItemID, e.Body["delivery_id"]); err != nil {
			return err
		}
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		return nil
	}
	return queueEventTurn(ctx, x, tx, id, eventID, e.Kind, nil)
}
