package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// wakesMain deliberately excludes progress, heartbeats and raw run failures:
// actionable failures already open a decision. The main agent need not wake
// itself for a decision it just opened or answered in its current turn.
func wakesMain(p protocol.Principal, kind string) bool {
	switch kind {
	case "task.completed":
		return true
	case "decision.opened", "decision.answered":
		return p.Kind != protocol.PrincipalMainAgent || p.Addon()
	default:
		return false
	}
}

// queueEventTurn runs inside the originating request transaction. Its child
// request ID and the existing main_turns record make dispatch/restart replay
// follow the same no-blind-retry rules as an owner message.
func queueEventTurn(ctx context.Context, x *Exec, tx *store.Tx, messageID, eventID, kind string, answer json.RawMessage) error {
	payload, err := json.Marshal(HandleMessage{MessageID: messageID, EventID: eventID, EventKind: kind, DecisionAnswer: answer})
	if err != nil {
		return fmt.Errorf("app: encode event wake: %w", err)
	}
	return tx.InsertOutbox(ctx, store.OutboxRow{
		OutboxID: x.IDs.New(protocol.PrefixOutbox), RequestID: x.IDs.New(protocol.PrefixRequest),
		ParentRequestID: x.Env.RequestID, Destination: MainThreadDestination, Operation: OpHandleEvent,
		PayloadJSON: string(payload), CreatedAt: x.Now.UnixMilli(),
	})
}

func recordDecisionWake(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
	d, err := store.GetDecision(ctx, tx.Q(), e.ObjectID)
	if err != nil {
		return err
	}
	state := "有待决事项"
	if e.Kind == "decision.answered" {
		state = "待决事项已有回答"
	}
	body := fmt.Sprintf("%s：%s\n\n%s", state, d.DecisionID, d.Question)
	if d.Worker != nil && e.Kind == "decision.opened" {
		role := "实现"
		if d.Worker.Role == protocol.RoleAccept {
			role = "验收"
		}
		body = fmt.Sprintf("%s worker 需要你的回答：\n\n%s", role, d.Question)
		if d.Worker.Evidence != "" {
			body += "\n\n调查证据与背景：\n" + d.Worker.Evidence
		}
		body += "\n\n回答后原任务继续；等待期间仍占用执行 slot。"
	}
	var recordedAnswer json.RawMessage
	if e.Kind == "decision.answered" {
		// A taskless answered decision may no longer appear in the current
		// snapshot. Preserve its exact answer in the durable delivery while
		// presenting readable facts in the owner's chat.
		recordedAnswer = json.RawMessage(d.AnswerJSON)
		var answer decisionAnswer
		if err := json.Unmarshal(recordedAnswer, &answer); err != nil {
			return fmt.Errorf("app: decode decision wake answer: %w", err)
		}
		if answer.Option != "" {
			body += "\n\n选择：" + answer.Option
		}
		if answer.Text != "" {
			body += "\n\n补充：" + answer.Text
		}
		if answer.Effect != nil {
			label := map[protocol.DecisionEffect]string{
				protocol.EffectImplement: "重新实现", protocol.EffectAccept: "重新验收",
				protocol.EffectCancel: "取消任务", protocol.EffectStartOver: "重新开始",
			}[answer.Effect.Effect]
			if answer.Effect.Applied {
				body += "\n\n已执行：" + label
			} else {
				body += "\n\n未执行：" + label
				if answer.Effect.Reason != "" {
					body += "（" + answer.Effect.Reason + "）"
				}
			}
		}
	}
	id := x.IDs.New(protocol.PrefixMessage)
	replyTo := ""
	if d.Worker != nil {
		if e.Kind == "decision.opened" {
			id = d.AskedMessageID
		} else {
			replyTo = d.AnsweredMessageID
		}
	}
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, ReplyTo: replyTo, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	noticeType := "decision_opened"
	if e.Kind == "decision.answered" {
		noticeType = "decision_answered"
	}
	if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: id, NoticeType: noticeType}); err != nil {
		return err
	}
	for _, binding := range []struct{ kind, id string }{{"decision", d.DecisionID}, {"task", d.TaskID}, {"event", eventID}} {
		if binding.id != "" {
			if err := tx.BindMessage(ctx, id, binding.kind, binding.id, "coordination"); err != nil {
				return err
			}
		}
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{
		EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id,
		ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`,
		RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"system"}`, CreatedAt: x.Now.UnixMilli(),
	}); err != nil {
		return err
	}
	return queueEventTurn(ctx, x, tx, id, eventID, e.Kind, recordedAnswer)
}
