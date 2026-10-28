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
	acceptances, err := store.ListAcceptances(ctx, tx.Q(), taskID, task.CurrentArtifactID)
	if err != nil {
		return err
	}
	acceptanceID := ""
	for _, acceptance := range acceptances {
		if acceptance.Verdict == protocol.VerdictPassed && acceptance.SpecVersion == task.SpecVersion {
			acceptanceID = acceptance.AcceptanceID
		}
	}
	if acceptanceID == "" {
		return fmt.Errorf("app: completed task has no passed acceptance")
	}
	var body strings.Builder
	fmt.Fprintf(&body, "任务已完成：%s\n\n独立验收通过 · spec %d。\n\n产物：`%s`。", taskID, task.SpecVersion, task.CurrentArtifactID)
	fmt.Fprintf(&body, "\n\n验收证据：`%s`（acceptance_id）。", acceptanceID)
	scope, err := store.ResolveTaskIssueScope(ctx, tx.Q(), taskID)
	if err != nil {
		return err
	}
	for _, item := range scope.Responsibilities {
		if item.Status != "closed" {
			if err := tx.PutCompletionReview(ctx, item.WorkItemID, protocol.WorkItemCompletionReview{EventID: sourceEventID, TaskID: taskID, AcceptanceID: acceptanceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
		}
		if scope.Canonical {
			fmt.Fprintf(&body, "\n\n事项：%s（%s）。独立验收已通过，等待 owner 核对本事项剩余范围并记录完成核对。任务完成不会自动关闭事项。", item.WorkItemID, item.Title)
			continue
		}
		fmt.Fprintf(&body, "\n\n工作项：%s（%s）。本轮执行完成不代表后续跟进结束；请核对已有承诺、交付状态和下一步安排；完成阶段后更新计划，仍有阶段责任时用 completion_reviews 明确记录核对结论。若已授权上线则继续；否则在核对现有选择后通过工作项选择卡询问下一步，明确尚需提交、PR、合入或部署的哪些步骤，已决定稍后处理的不要重复问。", item.WorkItemID, item.Title)
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
	if scope.Canonical {
		return nil
	} // Owner sees the durable pending review; no manager runtime is active.
	return queueEventTurn(ctx, x, tx, id, sourceEventID, "task.completed", nil)
}
