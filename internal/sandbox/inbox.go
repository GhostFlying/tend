package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
)

// seedInbox reproduces the resolved-history/pending-failure case through real
// owner commands and a scripted runner, with no production records or models.
func seedInbox(ctx context.Context, runtime *serverkit.Runtime, root string) (Demo, error) {
	var demo Demo
	if raw, err := os.ReadFile(filepath.Join(root, "demo.json")); err == nil {
		return demo, json.Unmarshal(raw, &demo)
	} else if !os.IsNotExist(err) {
		return demo, err
	}
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
	attention, err := storyAttention(ctx, runtime, issue.IssueID, task.TaskID)
	if err != nil {
		return demo, err
	}
	demo.Issues = []string{history.IssueID, issue.IssueID}
	demo.Tasks = []string{task.TaskID}
	demo.Decision = attention.AttentionID
	return demo, writeDemoReceipt(filepath.Join(root, "demo.json"), demo)
}
