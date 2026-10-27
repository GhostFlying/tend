package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A stalled failed root can retain its slot indefinitely. Deliver one durable
// fact to the existing coordinator; this does not cancel, retry or release it.
func (a *App) recordRootAttention(ctx context.Context, tx *store.Tx, run store.RunRow, e protocol.RunEvent) (bool, error) {
	if run.DesiredState != protocol.DesiredRun || (run.ObservedState != protocol.RunRunning && run.ObservedState != protocol.RunWaiting) {
		return false, nil
	}
	task, err := store.GetTask(ctx, tx.Q(), run.TaskID)
	if err != nil {
		return false, err
	}
	action, err := store.GetAction(ctx, tx.Q(), run.ActionID)
	if err != nil {
		return false, err
	}
	if action.TargetSpecVersion != task.SpecVersion || desiredFor(run, action, task) != protocol.DesiredRun {
		return false, nil
	}
	var fact struct {
		ManagedTurnAt int64  `json:"managed_turn_at"`
		Code          string `json:"code"`
		RootTurn      *struct {
			SessionID string `json:"sessionId"`
			TurnID    string `json:"turnId"`
		} `json:"root_turn"`
		TurnAt            int64 `json:"turn_at"`
		BackgroundPending bool  `json:"background_pending"`
	}
	if json.Unmarshal(e.Body, &fact) != nil || fact.TurnAt <= 0 || !fact.BackgroundPending || fact.Code == "observation_pending" {
		return false, nil
	}
	decisions, err := store.ListDecisions(ctx, tx.Q(), run.TaskID)
	if err != nil {
		return false, err
	}
	for _, d := range decisions {
		if d.Status == "open" {
			return false, nil
		}
	}
	now := a.now()
	key := strconv.FormatInt(fact.TurnAt, 10)
	if fact.RootTurn != nil && fact.RootTurn.SessionID != "" && fact.RootTurn.TurnID != "" {
		key = fact.RootTurn.SessionID + "/" + fact.RootTurn.TurnID
	}
	if fact.ManagedTurnAt > 0 {
		key = "managed:" + strconv.FormatInt(fact.ManagedTurnAt, 10)
	}
	fresh, err := tx.InsertInbox(ctx, "root-attention:"+run.RunID, key, now.UnixMilli())
	if err != nil || !fresh {
		return false, err
	}
	id, eventID := a.ids.New(protocol.PrefixMessage), a.ids.New(protocol.PrefixEvent)
	body := fmt.Sprintf("Worker 根回合失败，后台操作尚未确认收敛，需要核对：%s\n\n任务：%s。run 仍保留执行占用；本提醒未取消任务、释放槽位或重复外部操作。请根据当前账本及根回合观察处理。", run.RunID, run.TaskID)
	if e.Kind == "worker.root_observation_unknown" {
		body = fmt.Sprintf("Worker 根回合或后台操作事实无法确认，需要核对：%s\n\n任务：%s。未确认模型结束或后台操作结果，保留执行占用，不自动重试。", run.RunID, run.TaskID)
	}
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, CreatedAt: now.UnixMilli()})
	if err != nil {
		return false, err
	}
	if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: id, NoticeType: "run_attention"}); err != nil {
		return false, err
	}
	for _, binding := range []struct{ kind, id string }{{"task", run.TaskID}, {"run", run.RunID}, {"event", eventID}} {
		if err := tx.BindMessage(ctx, id, binding.kind, binding.id, "coordination"); err != nil {
			return false, err
		}
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: eventID, Source: "runner", ObjectType: "run", ObjectID: run.RunID, Kind: "run.attention_required", BodyJSON: string(e.Body), CreatedAt: now.UnixMilli()}); err != nil {
		return false, err
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`, CreatedAt: now.UnixMilli()}); err != nil {
		return false, err
	}
	x := &Exec{Env: &protocol.Envelope{}, Principal: protocol.Principal{Kind: protocol.PrincipalRun, Subject: run.RunID}, Now: now, IDs: a.ids}
	if err := queueEventTurn(ctx, x, tx, id, eventID, "run.attention_required", e.Body); err != nil {
		return false, err
	}
	return true, nil
}
