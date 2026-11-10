package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
	"github.com/GhostFlying/tend/internal/store"
)

// seedInbox reproduces the resolved-history/pending-failure case through real
// owner commands and a scripted runner, with no production records or models.
func seedInbox(ctx context.Context, runtime *serverkit.Runtime, root string, beforeBulk ...func(context.Context) (func(), error)) (Demo, error) {
	var demo Demo
	if raw, err := os.ReadFile(filepath.Join(root, "demo.json")); err == nil {
		return demo, json.Unmarshal(raw, &demo)
	} else if !os.IsNotExist(err) {
		return demo, err
	}
	release := func() {}
	if len(beforeBulk) > 0 {
		unlock, err := beforeBulk[0](ctx)
		if err != nil {
			return demo, err
		}
		release = unlock
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	s := seeder{app: runtime.App}
	var history protocol.Issue
	if err := s.execute(ctx, "inbox-history-issue", protocol.Target{Type: "issue"}, nil,
		protocol.CreateIssuePayload{Plan: plan("两份生产报告制作与收件确认", "demo-docs")},
		app.CanonicalOwnerIssueCommand{Action: "create"}, &history); err != nil {
		return demo, err
	}
	initial := int64(1)
	for n, title := range []string{"实现失败（missing_files）。这个任务接下来怎么办？", "报告文件已存在，需要确认如何处理路径冲突"} {
		key := "inbox-history-" + string(rune('a'+n))
		var attention protocol.Attention
		if err := s.execute(ctx, key, protocol.Target{Type: "attention"}, nil,
			protocol.CreateAttentionPayload{IssueID: history.IssueID, Kind: "decision", Title: title, Options: []string{"保留记录并确认已处理"}},
			app.AttentionCommand{Action: "create"}, &attention); err != nil {
			return demo, err
		}
		if err := s.execute(ctx, key+"-answer", protocol.Target{Type: "attention", ID: attention.AttentionID}, &initial,
			protocol.RespondAttentionPayload{Version: initial, Option: "保留记录并确认已处理", Text: "历史示例已处理，保留原问题与答复，无需再次操作。"},
			app.AttentionCommand{Action: "respond"}, nil); err != nil {
			return demo, err
		}
	}
	var issue protocol.Issue
	if err := s.execute(ctx, "inbox-pwa-issue", protocol.Target{Type: "issue"}, nil,
		protocol.CreateIssuePayload{Plan: plan("移动端 PWA 安全区、附件间距与输入光标修复", "demo-web")},
		app.CanonicalOwnerIssueCommand{Action: "create"}, &issue); err != nil {
		return demo, err
	}
	if err := s.admit(ctx, "inbox-pwa", &issue); err != nil {
		return demo, err
	}
	payload := taskPayload("移动端 PWA 三项体验修复与证据交付", issue.AssignmentEpoch)
	payload.Spec.Goal = "复现并修复顶部安全区、附件间距和输入光标问题，交付复现与验证报告。沙盒演示首次失败及原问题的答复与历史保留。"
	payload.Spec.MaxImplementAttempts = 3
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := s.execute(ctx, "inbox-pwa-task", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision,
		payload, app.CanonicalOwnerTaskCreate{}, &task); err != nil {
		return demo, err
	}
	release()
	released = true
	attention, err := storyAttention(ctx, runtime, issue.IssueID, task.TaskID)
	if err != nil {
		return demo, err
	}
	// This scenario asks the human to choose the retry. Route the original
	// runner question through its real supervisor chain, preserving its ID.
	for hop := 0; hop < 2; hop++ {
		var current protocol.Attention
		if err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			current, err = store.GetAttention(ctx, tx.Q(), attention.AttentionID)
			return err
		}); err != nil {
			return demo, err
		}
		if current.Responsibility == nil || current.Responsibility.RecipientKind == "owner" {
			break
		}
		r := current.Responsibility
		if err := s.execute(ctx, fmt.Sprintf("inbox-pwa-escalate-%d", hop), protocol.Target{Type: "attention", ID: attention.AttentionID}, &r.Revision, protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: r.AssignmentEpoch, Reason: "The inbox demonstration requires the human to choose the original retry"}, app.HandleAttention{Escalate: true}, nil); err != nil {
			return demo, err
		}
	}
	demo.Issues = []string{history.IssueID, issue.IssueID}
	demo.Tasks = []string{task.TaskID}
	demo.Decision = attention.AttentionID
	return demo, writeDemoReceipt(filepath.Join(root, "demo.json"), demo)
}
