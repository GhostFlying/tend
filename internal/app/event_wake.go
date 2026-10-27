package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	case "decision.opened", "decision.answered", "work_item_choice.answered":
		return p.Kind != protocol.PrincipalMainAgent
	default:
		return false
	}
}

// queueEventTurn runs inside the originating request transaction. Its child
// request ID and the existing main_turns record make dispatch/restart replay
// follow the same no-blind-retry rules as an owner message.
func queueEventTurn(ctx context.Context, x *Exec, tx *store.Tx, messageID, eventID, kind string, answer json.RawMessage) error {
	return queueBackgroundSource(ctx, x, tx, messageID, eventID, kind, answer)
}
func queueSingleEventTurn(ctx context.Context, x *Exec, tx *store.Tx, messageID, eventID, kind string, answer json.RawMessage) error {
	payload, err := json.Marshal(HandleMessage{MessageID: messageID, EventID: eventID, EventKind: kind, DecisionAnswer: answer})
	if err != nil {
		return fmt.Errorf("app: encode event wake: %w", err)
	}
	conversation, err := store.MessageConversation(ctx, tx.Q(), messageID)
	if err != nil {
		return err
	}
	return tx.InsertOutbox(ctx, store.OutboxRow{
		OutboxID: x.IDs.New(protocol.PrefixOutbox), RequestID: x.IDs.New(protocol.PrefixRequest),
		ParentRequestID: x.Env.RequestID, Destination: store.ConversationDestination(conversation), Operation: OpHandleEvent,
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
	if d.Worker == nil && e.Kind == "decision.opened" {
		summary, err := startupFailureNotice(ctx, x, tx.Q(), d.TaskID)
		if err != nil {
			return err
		}
		if summary != "" {
			body += "\n\n" + summary
		}
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
	if e.Kind == "decision.answered" && d.AnsweredMessageID != "" {
		conversation, err := store.MessageConversation(ctx, tx.Q(), d.AnsweredMessageID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO conversation_return_links(message_id,conversation_id,source_message_id) VALUES(?,?,?)`, id, conversation, d.AnsweredMessageID); err != nil {
			return err
		}
		return queueSingleEventTurn(ctx, x, tx, id, eventID, e.Kind, recordedAnswer)
	}
	return queueEventTurn(ctx, x, tx, id, eventID, e.Kind, recordedAnswer)
}

// recordWorkItemChoiceWake delivers durable answer facts without presenting the
// notification as a new owner message or replaying the owner's original turn.
func recordWorkItemChoiceWake(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
	c, err := store.GetWorkItemChoice(ctx, tx.Q(), e.ObjectID)
	if err != nil {
		return err
	}
	answer, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("app: encode choice answer: %w", err)
	}
	id := x.IDs.New(protocol.PrefixMessage)
	now := x.Now.UnixMilli()
	body := fmt.Sprintf("工作项下一步选择已有回答：%s\n\n%s\n\n回答：%s", c.ChoiceID, c.Question, c.Answer)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, ReplyTo: c.AnswerSourceID, CreatedAt: now})
	if err != nil {
		return err
	}
	for _, binding := range []struct{ kind, id string }{{"work_item_choice", c.ChoiceID}, {"work_item", c.WorkItemID}, {"event", eventID}} {
		if err := tx.BindMessage(ctx, id, binding.kind, binding.id, "notice"); err != nil {
			return err
		}
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"system"}`, CreatedAt: now}); err != nil {
		return err
	}
	// This notice coordinates a new owner answer.
	// Review the original in the main logical turn before releasing background
	// work against its changed context; never treat the notice as new authority.
	conversation, err := store.MessageConversation(ctx, tx.Q(), c.AnswerSourceID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO conversation_return_links(message_id,conversation_id,source_message_id) VALUES(?,?,?)`, id, conversation, c.AnswerSourceID); err != nil {
		return err
	}
	return queueSingleEventTurn(ctx, x, tx, id, eventID, e.Kind, answer)
}

// startupFailureNotice uses only the run reporting this failure. Earlier runs
// and previously recovered failures cannot explain a new decision.
func startupFailureNotice(ctx context.Context, x *Exec, q store.Querier, taskID string) (string, error) {
	if x.Env.Operation != protocol.OpRunsRelease || x.Env.Target.Type != "run" || taskID == "" {
		return "", nil
	}
	var release protocol.ReleasePayload
	if json.Unmarshal(x.Env.Payload, &release) != nil || release.Reason != "worker_start_failed" {
		return "", nil
	}
	run, err := store.GetRun(ctx, q, x.Env.Target.ID)
	if err != nil {
		return "", err
	}
	if run.TaskID != taskID || run.ObservedState != protocol.RunFailed {
		return "", nil
	}
	var kind, raw string
	err = q.QueryRowContext(ctx, `SELECT kind,body_json FROM run_logs WHERE run_id=? AND kind IN ('worker.startup_failed','worker.startup_retry_pending','worker.startup_started') ORDER BY seq DESC LIMIT 1`, run.RunID).Scan(&kind, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if kind != "worker.startup_failed" {
		return "", nil
	}
	var fact struct {
		Attempts   int `json:"attempts"`
		Diagnostic struct {
			Code  string `json:"code"`
			Phase string `json:"phase"`
		} `json:"diagnostic"`
	}
	if json.Unmarshal([]byte(raw), &fact) != nil {
		return "", nil
	}
	// Run logs may contain arbitrary strings. Render only fixed descriptions;
	// raw errors, terminal snapshots, prompts and provider identities stay out.
	reason := map[string]string{"readiness_timeout": "等待 CLI 就绪超时", "interactive_prompt": "CLI 等待交互处理", "process_exited": "CLI 进程已退出", "readiness_failed": "CLI 就绪检查失败", "write_failed": "提示写入失败", "delivery_unknown": "提示投递结果未确认"}[fact.Diagnostic.Code]
	phase := map[string]string{"readiness": "等待 CLI 就绪", "write": "写入任务提示", "submit": "提交任务提示"}[fact.Diagnostic.Phase]
	if reason == "" || phase == "" {
		return "", nil
	}
	if fact.Attempts < 0 {
		fact.Attempts = 0
	}
	return fmt.Sprintf("启动失败：%s。阶段：%s；已自动重试 %d 次。", reason, phase, fact.Attempts), nil
}
