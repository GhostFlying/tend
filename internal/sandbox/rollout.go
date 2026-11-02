package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
)

// seedRolloutDemo exercises the real task/run pages using scripted workers.
func seedRolloutDemo(ctx context.Context, runtime *serverkit.Runtime, root string) (Demo, error) {
	receipt := filepath.Join(root, "rollout-demo.json")
	var demo Demo
	if raw, err := os.ReadFile(receipt); err == nil {
		err = json.Unmarshal(raw, &demo)
		return demo, err
	}
	s := seeder{app: runtime.App}
	var issue protocol.Issue
	if err := s.execute(ctx, "rollout-issue", protocol.Target{Type: "issue"}, nil, protocol.CreateIssuePayload{Plan: plan("移动端 PWA 体验修复 · 过程阅读沙箱", "demo-web")}, app.CanonicalOwnerIssueCommand{Action: "create"}, &issue); err != nil {
		return demo, err
	}
	demo.Parent = issue.IssueID
	demo.Issues = []string{issue.IssueID}
	// Set work intent before a concurrent runner claim can advance revisions.
	if err := s.execute(ctx, "rollout-issue-start-status", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress}, app.CanonicalOwnerIssueCommand{Action: "set_status"}, &issue); err != nil {
		return demo, err
	}
	for n, title := range []string{"移动端 PWA 三项体验修复（沙箱）", "移动端 PWA：失败过程排查（沙箱）", "移动端 PWA：长输出与历史分页（沙箱）"} {
		var task struct {
			TaskID string `json:"task_id"`
		}
		if err := s.execute(ctx, fmt.Sprintf("rollout-task-%d", n), protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, taskPayload(title, issue.AssignmentEpoch), app.CanonicalOwnerTaskCreate{}, &task); err != nil {
			return demo, err
		}
		demo.Tasks = append(demo.Tasks, task.TaskID)
		issue.Revision++
	}

	return demo, writeDemoReceipt(receipt, demo)
}

func rolloutSampleSteps(role protocol.RunRole, goal string) []map[string]any {
	if role == protocol.RoleAccept {
		return []map[string]any{
			{"type": "agentMessage", "text": "我会独立核对这轮交付，检查安全区、横屏布局和 PWA 启动页。以下检查由沙箱脚本模拟。"},
			{"type": "mcpToolCall", "tool": "读取文件", "arguments": map[string]string{"path": "sandbox-report.md"}, "result": "已读取沙箱交付报告。"},
			{"type": "commandExecution", "command": "pnpm check", "aggregatedOutput": "TypeScript  passed\nLint        passed\nBuild       passed\n\n以上为沙箱样例输出，未运行实际检查命令。", "exitCode": 0},
			{"type": "agentMessage", "text": "沙箱验收已通过。正式验收还需要在实际设备上验证，当前记录只演示界面和交互。"},
		}
	}
	steps := []map[string]any{
		{"type": "agentMessage", "text": "我先检查移动端页面的布局、底部安全区，以及 PWA 启动时的显示方式。"},
		{"type": "webSearch", "query": "safe-area / viewport", "result": "找到页面顶部与底部的安全区声明。这里是沙箱样例，没有发起网络搜索。"},
		{"type": "mcpToolCall", "tool": "读取文件", "arguments": map[string]string{"path": "PageHeader.module.css"}, "result": ".header {\n  padding-top: env(safe-area-inset-top);\n}"},
		{"type": "mcpToolCall", "tool": "读取文件", "arguments": map[string]string{"path": "AppShell.tsx"}, "result": "<main className={styles.content}>\n  {children}\n</main>"},
		{"type": "agentMessage", "text": "页面内容已经避开顶部安全区，但底部操作区域仍需要保留空间。我会一起检查竖屏和横屏。"},
		{"type": "fileChange", "changes": "PageHeader.module.css\n+ padding-bottom: env(safe-area-inset-bottom);", "status": "completed"},
		{"type": "commandExecution", "command": "pnpm check", "aggregatedOutput": "TypeScript  passed\nLint        passed\nBuild       passed\n\n沙箱脚本输出：未修改实际源码，也未执行以上命令。", "exitCode": 0},
		{"type": "agentMessage", "text": "正在整理修改与验证记录。\n\n这是一组交互沙箱样例，用来检查过程阅读、工具展开和返回位置。"},
	}
	if strings.Contains(goal, "长输出") {
		var output strings.Builder
		for n := 0; n < 160; n++ {
			steps = append(steps, map[string]any{"type": "mcpToolCall", "tool": "读取文件", "arguments": map[string]string{"path": fmt.Sprintf("layout/case-%03d.css", n+1)}, "result": "沙箱布局样例：安全区与长标题检查通过。"})
			fmt.Fprintf(&output, "case %03d  viewport=390x844  safe-area=ok  wrap=ok\n", n+1)
		}
		steps = append(steps, map[string]any{"type": "commandExecution", "command": "sandbox layout-check --all", "aggregatedOutput": strings.Repeat(output.String(), 24), "exitCode": 0})
	}
	return steps
}
