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
	Issues   []string `json:"issues"`
	Tasks    []string `json:"tasks"`
	Decision string   `json:"decision"`
	Parent   string   `json:"parent"`
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
	receipt := filepath.Join(root, "demo.json")
	var demo Demo
	if raw, err := os.ReadFile(receipt); err == nil {
		if err = json.Unmarshal(raw, &demo); err != nil {
			return demo, fmt.Errorf("read sandbox receipt: %w", err)
		}
		return demo, nil
	} else if !os.IsNotExist(err) {
		return demo, err
	}
	s := seeder{app: runtime.App}
	names := []string{"收集下一轮体验优化建议", "完善手机端导航与长标题换行：在连续对话、Issue 看板、执行记录和收件箱之间切换时，保持未发送草稿、原始消息来源、输入焦点与浏览器返回位置，并确认较长中文内容不会被截断", "正在验证真实消息发送与可重试请求", "评审交付说明与独立验收证据", "已完成示例：检查关闭记录与归档展示", "已取消示例：优先级调整后停止的探索"}
	for n, status := range protocol.IssueWorkStatusValues() {
		var issue protocol.Issue
		key := fmt.Sprintf("board-%d", n)
		p := protocol.CreateIssuePayload{Plan: plan(names[n], "demo-web")}
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
	for _, item := range []protocol.CreateAttentionPayload{{Kind: "decision", Title: "界面验收先看手机还是桌面？", Options: []string{"手机优先", "桌面优先"}}, {Kind: "action", Title: "请确认示例交付物的阅读体验"}, {Kind: "information", Title: "沙盒不会连接真实模型或生产仓库"}} {
		var attention protocol.Attention
		if err := s.execute(ctx, "attention-"+item.Kind, protocol.Target{Type: "attention"}, nil, item, app.AttentionCommand{Action: "create"}, &attention); err != nil {
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
	raw, err := json.MarshalIndent(demo, "", "  ")
	if err != nil {
		return demo, err
	}
	temporary := receipt + ".tmp"
	if err := os.WriteFile(temporary, raw, 0600); err != nil {
		return demo, err
	}
	if err := os.Rename(temporary, receipt); err != nil {
		return demo, err
	}
	return demo, nil
}
