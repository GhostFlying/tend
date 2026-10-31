package sandbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

const storyTitle = "创建 多轮故事：手机布局从需求澄清到独立验收"

// Every round is an owner command followed by a real hosted tool turn. Reads
// below are synchronization only; no business state is injected into SQLite.
func seedStory(ctx context.Context, runtime *serverkit.Runtime, demo *Demo) error {
	s := seeder{app: runtime.App}
	// These newly created decisions start at revision/version one; replay must
	// reuse their original envelope even after the answer advances the view.
	initial := int64(1)
	// A metadata-only repair can settle an interrupted source without doing
	// its business step. Check the durable result before advancing and issue
	// deterministic recovery sources only when the result is still missing.
	for attempt := 0; attempt < 4; attempt++ {
		exists := false
		if err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE json_extract(w.body_json,'$.title')=?)`, storyTitle).Scan(&exists)
		}); err != nil {
			return err
		}
		if exists {
			break
		}
		key := "story-create"
		if attempt > 0 {
			key = fmt.Sprintf("story-create-recovery-%d", attempt)
		}
		if err := s.storyMessage(ctx, runtime, key, "", storyTitle); err != nil {
			return err
		}
	}
	var issue protocol.IssueView
	if err := storyWait(ctx, "manager assignment", func() (bool, error) {
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			var id string
			if err := tx.Q().QueryRowContext(ctx, `SELECT issue_id FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE json_extract(w.body_json,'$.title')=?`, storyTitle).Scan(&id); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			var err error
			issue, err = views.CanonicalIssue(ctx, tx.Q(), id, 0)
			return err
		})
		return err == nil && issue.Conversation != nil && issue.Conversation.CanSend && issue.Progress != nil, err
	}); err != nil {
		return err
	}
	demo.Story = issue.IssueID
	conversation := issue.Conversation.ConversationID
	rounds := []string{
		"范围澄清：先看手机上的长标题和返回焦点，暂不改桌面布局。",
		"验收条件补充：保留未发送草稿与返回焦点，触控入口不挤压标题，收件箱决定能够阅读完整说明。",
		"请详细走查这轮输入：逐项读取上下文，给出可展开的检查过程和完整说明。",
		"问我：手机布局和桌面布局，应该优先检查哪一种？",
	}
	for n, text := range rounds {
		if err := s.storyMessage(ctx, runtime, fmt.Sprintf("story-round-%d", n), conversation, text); err != nil {
			return err
		}
	}
	attention, err := storyAttention(ctx, runtime, issue.IssueID, "")
	if err != nil {
		return err
	}
	demo.StoryDecision = attention.AttentionID
	if err := s.execute(ctx, "story-answer", protocol.Target{Type: "attention", ID: attention.AttentionID}, &initial, protocol.RespondAttentionPayload{Version: initial, Option: "手机布局", Text: "手机优先，桌面放在下一轮。"}, app.AttentionCommand{Action: "respond"}, nil); err != nil {
		return err
	}
	if err := s.storyMessage(ctx, runtime, "story-choice-confirmed", conversation, "决定已确认：手机优先，保留上述草稿和返回焦点条件。"); err != nil {
		return err
	}
	if err := s.storyMessage(ctx, runtime, "story-dispatch", conversation, "派发 首次失败后重试：检查手机布局并交付可下载报告"); err != nil {
		return err
	}
	var task string
	if err := storyWait(ctx, "story task", func() (bool, error) {
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			current, err := views.CanonicalIssue(ctx, tx.Q(), issue.IssueID, 0)
			if err == nil && len(current.Tasks) > 0 {
				task = current.Tasks[0].TaskID
			}
			return err
		})
		return task != "", err
	}); err != nil {
		return err
	}
	demo.StoryTask = task
	failure, err := storyAttention(ctx, runtime, issue.IssueID, task)
	if err != nil {
		return err
	}
	if err := s.storyMessage(ctx, runtime, "story-failure-explained", conversation, "第一轮检查异常退出了，请保留失败记录；我同意修正输入后再试。"); err != nil {
		return err
	}
	if err := s.execute(ctx, "story-retry", protocol.Target{Type: "attention", ID: failure.AttentionID}, &initial, protocol.RespondAttentionPayload{Version: initial, Option: "retry the implementation", Text: "保留原要求，在同一任务上重试。"}, app.AttentionCommand{Action: "respond"}, nil); err != nil {
		return err
	}
	if err := storyWait(ctx, "independent acceptance", func() (bool, error) {
		var detail protocol.TaskDetail
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error { var err error; detail, err = views.Task(ctx, tx.Q(), task); return err })
		return detail.Lifecycle == protocol.TaskCompleted, err
	}); err != nil {
		return err
	}
	for n, text := range []string{
		"总结：手机布局、长标题、草稿和返回焦点均已完成这轮核对，保留失败与重试历史。",
	} {
		if err := s.storyMessage(ctx, runtime, fmt.Sprintf("story-summary-%d", n), conversation, text); err != nil {
			return err
		}
	}
	return nil
}

func (s seeder) storyMessage(ctx context.Context, runtime *serverkit.Runtime, key, conversation, text string) error {
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := s.execute(ctx, key, protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{ConversationID: conversation, Body: text}, app.SubmitMessage{}, &result); err != nil {
		return err
	}
	return storyWait(ctx, key, func() (bool, error) {
		complete := false
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_messages m JOIN logical_turns l USING(logical_turn_id) JOIN logical_turn_executions e USING(logical_turn_id) JOIN main_turns t ON t.request_id=e.request_id WHERE m.message_id=? AND l.status='completed' AND t.status='completed')`, result.MessageID).Scan(&complete)
		})
		return complete, err
	})
}
func storyWait(ctx context.Context, label string, read func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		complete, err := read()
		if err != nil {
			return fmt.Errorf("seed story %s: %w", label, err)
		}
		if complete {
			return nil
		}
		if !delay(ctx, 100*time.Millisecond) {
			return fmt.Errorf("seed story %s: %w", label, ctx.Err())
		}
	}
}
func storyAttention(ctx context.Context, runtime *serverkit.Runtime, issue, task string) (protocol.AttentionView, error) {
	var found protocol.AttentionView
	err := storyWait(ctx, "decision", func() (bool, error) {
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			page, err := views.Attentions(ctx, tx.Q(), views.AttentionFilter{IssueID: issue, Kind: "decision", IncludeArchived: true})
			if err != nil {
				return err
			}
			for _, item := range page.Items {
				if item.TaskID == task {
					found = item
					break
				}
			}
			return nil
		})
		return found.AttentionID != "", err
	})
	return found, err
}
