package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// recordCompletionNotice appends a factual system message in the same
// transaction as its completion event and queues its orchestration turn.
// Request deduplication and the domain's completion transition make this
// one notice per completion, including when an acceptance is reported again.
func recordCompletionNotice(ctx context.Context, x *Exec, tx *store.Tx, taskID, sourceEventID string) error {
	task, err := store.GetTask(ctx, tx.Q(), taskID)
	if err != nil {
		return err
	}
	ref, err := store.GetArtifactRef(ctx, tx.Q(), task.CurrentArtifactID)
	if err != nil {
		return err
	}
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(ref), &artifact); err != nil {
		return fmt.Errorf("app: decode completed artifact: %w", err)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "任务已完成：%s\n\n独立验收通过 · spec %d。\n\n产物：`%s`。", taskID, task.SpecVersion, task.CurrentArtifactID)
	items, err := store.ListTaskWorkItems(ctx, tx.Q(), taskID)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Fprintf(&body, "\n\n工作项：%s（%s）。本轮执行完成不代表后续跟进结束；请核对已有承诺、交付状态和下一步安排。", item.WorkItemID, item.Title)
	}
	for _, mount := range artifact.Mounts {
		// Commit hashes are facts captured in the accepted artifact, not
		// a claim about the current working tree or remote delivery.
		fmt.Fprintf(&body, "\n\n提交：`%s`", mount.CommitSHA)
		if mount.SnapshotSHA != "" {
			fmt.Fprintf(&body, " · 工作树快照：`%s`", mount.SnapshotSHA)
		}
	}
	if artifact.Report != nil {
		fmt.Fprint(&body, "\n\n报告已保存，可在任务详情查看。")
	}
	for _, file := range artifact.Files {
		fmt.Fprintf(&body, "\n\n交付文件：%s（%d 字节）。", file.Name, file.Size)
	}
	now := x.Now.UnixMilli()
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body.String(), CreatedAt: now})
	if err != nil {
		return err
	}
	if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: id, NoticeType: "task_completed"}); err != nil {
		return err
	}
	for _, binding := range []struct{ objectType, objectID string }{
		{"task", taskID}, {"artifact", task.CurrentArtifactID}, {"event", sourceEventID},
	} {
		if err := tx.BindMessage(ctx, id, binding.objectType, binding.objectID, "completion"); err != nil {
			return err
		}
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{
		EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id,
		ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`,
		RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"system"}`, CreatedAt: now,
	})
	if err != nil {
		return err
	}
	return queueEventTurn(ctx, x, tx, id, sourceEventID, "task.completed", nil)
}
