package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
)

// Demo is the stable seed receipt, not a replacement for live owner views.
type Demo struct {
	Issues        []string `json:"issues"`
	Tasks         []string `json:"tasks"`
	Decision      string   `json:"decision"`
	Story         string   `json:"story,omitempty"`
	StoryTask     string   `json:"story_task,omitempty"`
	StoryDecision string   `json:"story_decision,omitempty"`
	Parent        string   `json:"parent"`
}

type seeder struct{ app *app.App }

func seedRequest(key string) string {
	digest := sha256.Sum256([]byte("sandbox-demo-v1/" + key))
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	body := []byte("00000000000000000000000000")
	for n := 1; n < len(body); n++ {
		body[n] = alphabet[digest[n]&31]
	}
	return "req_" + string(body)
}

func (s seeder) execute(ctx context.Context, key string, target protocol.Target, revision *int64, payload any, command app.Command, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := s.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: localOwner, Client: "sandbox-seed"}, &protocol.Envelope{RequestID: seedRequest(key), Operation: command.Operation(), Target: target, ExpectedControlRevision: revision, Payload: raw}, command)
	if err != nil {
		return fmt.Errorf("seed %s: %w", key, err)
	}
	if result.Status == protocol.OpFailed {
		return fmt.Errorf("seed %s: %w", key, result.Error)
	}
	if out != nil {
		return json.Unmarshal(result.Result, out)
	}
	return nil
}

func seedCategories(ctx context.Context, a *app.App, _ protocol.IDGen) error {
	s := seeder{app: a}
	zero := int64(0)
	for _, entry := range []struct{ id, title string }{{"sandbox-planner", "沙盒协调与咨询"}, {protocol.DefaultIssueManagerCategory, "沙盒 Issue 负责人"}} {
		if err := s.execute(ctx, "category-"+entry.id, protocol.Target{Type: "category", ID: entry.id}, &zero, protocol.PutAgentCategoryPayload{Title: entry.title, State: "active"}, app.PutAgentCategory{}, nil); err != nil {
			return err
		}
	}
	return nil
}

// SeedDemo only uses real app commands, each with a stable request identity.
// Replaying a partial seed reuses exact payloads and original returned revisions.
func SeedDemo(ctx context.Context, runtime *serverkit.Runtime, root string) (Demo, error) {
	return seedDemo(ctx, runtime, root, false, false)
}

func seedDemo(ctx context.Context, runtime *serverkit.Runtime, root string, skipStory, storyOnly bool) (Demo, error) {
	receipt := filepath.Join(root, "demo.json")
	var demo Demo
	if raw, err := os.ReadFile(receipt); err == nil {
		if err = json.Unmarshal(raw, &demo); err != nil {
			return demo, fmt.Errorf("read sandbox receipt: %w", err)
		}
		if demo.Story == "" && !skipStory {
			if err := seedStory(ctx, runtime, &demo); err != nil {
				return demo, err
			}
			if err := writeDemoReceipt(receipt, demo); err != nil {
				return demo, err
			}
		}
		return demo, nil
	} else if !os.IsNotExist(err) {
		return demo, err
	}
	s := seeder{app: runtime.App}
	// Build the conversation story before broad board/task seeds enqueue
	// unrelated notifications; its turns retain their ordinary app ordering.
	if !skipStory {
		if err := seedStory(ctx, runtime, &demo); err != nil {
			return demo, err
		}
	}
	if storyOnly {
		return demo, writeDemoReceipt(receipt, demo)
	}
	names := []string{"收集下一轮体验优化建议", "完善手机端导航与长标题换行：在连续对话、Issue 看板、执行记录和收件箱之间切换时，保持未发送草稿、原始消息来源、输入焦点与浏览器返回位置，并确认较长中文内容不会被截断", "正在验证真实消息发送与可重试请求", "评审交付说明与独立验收证据", "已完成示例：检查关闭记录与归档展示", "已取消示例：优先级调整后停止的探索"}
	for n, status := range protocol.IssueWorkStatusValues() {
		var issue protocol.Issue
		key := fmt.Sprintf("board-%d", n)
		p := protocol.CreateIssuePayload{Plan: plan(names[n], "demo-web")}
		if !skipStory && n != 2 {
			p.Assignee = &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "sandbox-hosted/manager"}
		}
		if err := s.execute(ctx, key, protocol.Target{Type: "issue"}, nil, p, app.CanonicalOwnerIssueCommand{Action: "create"}, &issue); err != nil {
			return demo, err
		}
		demo.Issues = append(demo.Issues, issue.IssueID)
		if err := s.execute(ctx, key+"-status", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}, app.CanonicalOwnerIssueCommand{Action: "set_status"}, nil); err != nil {
			return demo, err
		}
	}
	var parent protocol.Issue
	if err := s.execute(ctx, "parent", protocol.Target{Type: "issue"}, nil, protocol.CreateIssuePayload{Plan: plan("端到端交付体验：从对话到验收", "demo-service")}, app.CanonicalOwnerIssueCommand{Action: "create"}, &parent); err != nil {
		return demo, err
	}
	demo.Parent = parent.IssueID
	for n, title := range []string{"核对进展消息", "核对交付物与验收结论"} {
		if err := s.execute(ctx, fmt.Sprintf("child-%d", n), protocol.Target{Type: "issue"}, nil, protocol.CreateIssuePayload{Plan: plan(title, "demo-service"), ParentIssueID: parent.IssueID, ExpectedParentRevision: &parent.Revision}, app.CanonicalOwnerIssueCommand{Action: "create"}, nil); err != nil {
			return demo, err
		}
	}
	// These tasks are advanced by the real in-process runner, not injected states.
	for n, title := range []string{"示例报告：完成输入检查与交付", "失败示例：保留真实执行错误", "问我：选择布局后继续生成报告"} {
		var issue protocol.Issue
		key := fmt.Sprintf("execution-%d", n)
		p := protocol.CreateIssuePayload{Plan: plan(title, "demo-docs")}
		if n == 0 {
			p.Assignee = &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "sandbox-hosted/manager"}
		}
		if err := s.execute(ctx, key, protocol.Target{Type: "issue"}, nil, p, app.CanonicalOwnerIssueCommand{Action: "create"}, &issue); err != nil {
			return demo, err
		}
		if err := s.execute(ctx, key+"-progress", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "示例要求已确认，等待脚本主机执行。", NextStep: "检查报告与独立验收"}, app.UpdateIssueProgress{}, nil); err != nil {
			return demo, err
		}
		issue.Revision++
		var task struct {
			TaskID string `json:"task_id"`
		}
		if err := s.execute(ctx, key+"-task", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, taskPayload(title, issue.AssignmentEpoch), app.CanonicalOwnerTaskCreate{}, &task); err != nil {
			return demo, err
		}
		demo.Tasks = append(demo.Tasks, task.TaskID)
		issue.Revision++
		if err := s.execute(ctx, key+"-status", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress}, app.CanonicalOwnerIssueCommand{Action: "set_status"}, nil); err != nil {
			return demo, err
		}
	}

	for _, entry := range []struct {
		payload protocol.CreateAttentionPayload
		body    string
	}{
		{protocol.CreateAttentionPayload{Kind: "decision", Title: "界面验收先看手机还是桌面？", Options: []string{"手机优先", "桌面优先"}}, "手机上的长标题和收件箱布局已准备好。请决定本轮先检查手机触控与返回焦点，还是桌面看板的列宽；另一端留到下一轮。"},
		{protocol.CreateAttentionPayload{Kind: "action", Title: "请确认示例交付物的阅读体验"}, "请打开示例报告，检查段落、代码块和下载入口是否清晰。若阅读体验没有问题，请在这里确认；发现问题时请写明页面与具体位置。"},
		{protocol.CreateAttentionPayload{Kind: "information", Title: "沙盒不会连接真实模型或生产仓库"}, "当前任务由进程内脚本执行，报告与附件只写入独立沙盒目录。你可以放心尝试派发、回答决定和重置；这条说明无需回复，也不会阻止 Issue 关闭。"},
	} {
		item := entry.payload
		var source protocol.ManualSource
		if err := s.execute(ctx, "attention-source-"+item.Kind, protocol.Target{Type: "message"}, nil, protocol.InjectSourcePayload{Text: entry.body, Metadata: protocol.ManualSourceMetadata{Author: "沙盒体验走查"}}, app.InjectSource{}, &source); err != nil {
			return demo, err
		}
		item.SourceMessageID = source.MessageID
		var attention protocol.Attention
		if err := s.execute(ctx, "attention-described-"+item.Kind, protocol.Target{Type: "attention"}, nil, item, app.AttentionCommand{Action: "create"}, &attention); err != nil {
			return demo, err
		}
		if item.Kind == "decision" {
			demo.Decision = attention.AttentionID
		}
	}

	if err := s.execute(ctx, "welcome", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "你好，请介绍一下这个交互沙盒。"}, app.SubmitMessage{}, nil); err != nil {
		return demo, err
	}
	input, output := int64(1200), int64(360)
	if err := runtime.App.RecordTokenUsage(ctx, "main", protocol.TokenUsageSample{Source: "sandbox-seed", RequestID: seedRequest("usage"), ThreadID: "sandbox-demo-history", Revision: 1, Settlement: "final", Model: model, Input: &input, Output: &output}); err != nil {
		return demo, err
	}
	return demo, writeDemoReceipt(receipt, demo)
}

func writeDemoReceipt(receipt string, demo Demo) error {
	raw, err := json.MarshalIndent(demo, "", "  ")
	if err != nil {
		return err
	}
	temporary := receipt + ".tmp"
	if err := os.WriteFile(temporary, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, receipt); err != nil {
		return err
	}
	return nil
}
