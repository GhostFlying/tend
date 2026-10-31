package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type toolCaller struct {
	ctx    context.Context
	thread *testkit.FakeThread
	turn   string
	call   int
	effect int64
	log    *slog.Logger
}

func (c *toolCaller) invoke(name string, args, out any) bool {
	c.call++
	id := fmt.Sprintf("%s_call_%d", c.turn, c.call)
	raw, _ := json.Marshal(args)
	c.thread.UseTool(c.turn, id, harness.ToolUse{Name: name, Input: string(raw), Status: protocol.ToolRunning})
	result := c.thread.CallTool(c.ctx, c.turn, id, name, args)
	status := protocol.ToolCompleted
	if !result.Success {
		status = protocol.ToolFailed
	}
	c.thread.UseTool(c.turn, id, harness.ToolUse{Name: name, Input: string(raw), Output: result.Text, Status: status})
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &response) != nil || !response.OK {
		c.log.Warn("sandbox tool failed", "tool", name, "result", result.Text)
		return false
	}
	return out == nil || json.Unmarshal(response.Result, out) == nil
}

func (c *toolCaller) mutate(name, target string, revision *int64, payload, out any) bool {
	c.effect++
	args := map[string]any{"effect_seq": c.effect, "payload": payload}
	if target != "" {
		args["target_id"] = target
	}
	if revision != nil {
		args["expected_control_revision"] = *revision
	}
	var result struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if !c.invoke(name, args, &result) {
		return false
	}
	return out == nil || json.Unmarshal(result.Outcome, out) == nil
}

func scriptedTurn(ctx context.Context, th *testkit.FakeThread, turn string, input harness.TurnInput, log *slog.Logger) string {
	c := toolCaller{ctx: ctx, thread: th, turn: turn, log: log}
	manager := false
	for _, tool := range th.Config.Tools {
		if tool.Name == "issues_turn_read" {
			manager = true
		}
	}
	read := "conversation_turn_read"
	if manager {
		read = "issues_turn_read"
	}
	var state protocol.LogicalTurnState
	if !c.invoke(read, struct{}{}, &state) {
		return "failed"
	}
	c.effect = state.NextEffectSeq - 1
	if c.effect < 0 {
		c.effect = 0
	}
	var issue protocol.IssueView
	if manager {
		if !c.reviewCurrentIssue(&issue) {
			return "failed"
		}
	}
	// Repair turns review metadata only; they never repeat business operations.
	respond := false
	createdIssue := ""
	response := "已收到。这是交互沙盒，所有消息与操作保存在独立账本中。可以试试“创建”“派发”或“问我”。"
	if !input.Repair {
		text := input.Text
		source := input.UserMessageID
		sourceRevision := int64(1)
		for _, message := range state.Messages {
			if message.MessageID == source {
				sourceRevision = message.ContentRevision
			}
		}
		if !manager {
			var page protocol.ConversationPage
			if !c.invoke("conversation_read", map[string]any{"limit": 50}, &page) {
				return "failed"
			}
			text = ""
			for _, item := range page.Items {
				if item.MessageID == source && item.Author == "owner" {
					text = item.Body
					respond = true
				}
			}
		}
		// Background delivery text includes system envelopes. Only persisted owner
		// sources may trigger work, so completion notifications cannot dispatch again.
		if manager {
			text, source = "", ""
			for _, pending := range issue.SourceReviews {
				if pending.Author == "owner" && !pending.Withdrawn {
					text, source = pending.Body, pending.MessageID
					respond = true
					sourceRevision = pending.ContentRevision
				}
			}
		}
		create := strings.Contains(text, "新建") || strings.Contains(text, "创建")
		if manager && create {
			// A root creation source is inherited by its new manager. Only an owner
			// message physically sent to this Issue can request another child, so a
			// delegated creation cannot recursively reproduce its own source.
			var page protocol.ConversationPage
			if !c.invoke("conversation_read", map[string]any{"limit": 50}, &page) {
				return "failed"
			}
			direct := false
			for _, item := range page.Items {
				if item.MessageID == source && item.Author == "owner" {
					direct = true
				}
			}
			create = direct
		}
		dispatch := strings.Contains(text, "派发") || strings.Contains(text, "执行")
		ask := strings.Contains(text, "问我")
		switch {
		case create || dispatch && !manager:
			title := shortTitle(text)
			p := protocol.CreateIssuePayload{Plan: plan(title, "demo-web"), IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: &sourceRevision}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "sandbox-hosted/manager"}}
			if manager {
				p.ParentIssueID = issue.IssueID
				p.ExpectedParentRevision = &issue.Revision
				p.Plan.Project = issue.Plan.Project
			}
			var created protocol.Issue
			if !c.mutate("issues_create", "", nil, p, &created) {
				return "failed"
			}
			createdIssue = created.IssueID
			response = "已创建 Issue，交给沙盒负责人继续跟进。"
			if dispatch {
				response = "已派发示例目标，负责人将创建执行任务；脚本主机会输出进展并交付结果。"
			}
		case dispatch && manager:
			if !c.mutate("issues_create_task", issue.IssueID, &issue.Revision, taskPayload(text, issue.AssignmentEpoch), nil) {
				return "failed"
			}
			response = "任务已派发，执行 Agent 正在准备示例交付物。"
		case ask:
			if !c.mutate("attentions_create", "", nil, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "示例方案：你希望先检查哪种布局？", Options: []string{"手机布局", "桌面布局"}, SourceMessageID: source}, nil) {
				return "failed"
			}
			response = "已在收件箱提出一个决定，请选择手机或桌面布局。"
		default:
			if manager {
				response = "已查看 Issue 的最新来源与执行进展，继续等待你的下一条消息。"
			}
		}
	}
	if respond && !input.Repair {
		if !streamReply(ctx, th, turn, response) {
			return "interrupted"
		}
		// Event persistence is asynchronous; the real coordinator performs its
		// metadata-only repair if a later final frame requires another review.

	}
	if manager {
		if !c.reviewCurrentIssue(&issue) {
			return "failed"
		}
	}
	if !c.invoke(read, struct{}{}, &state) {
		return "failed"
	}
	if !manager {
		for _, message := range state.Messages {
			if message.Disposition != "pending" {
				continue
			}
			disposition := "unassigned"
			ids := []string{}
			if createdIssue != "" {
				disposition = "linked"
				ids = append(ids, createdIssue)
			}
			if !c.mutate("conversation_associate_issues", message.MessageID, &message.Revision, protocol.SetIssueMessageAssociationsPayload{ContentRevision: message.ContentRevision, Disposition: disposition, IssueIDs: ids}, nil) {
				return "failed"
			}
		}
		if !c.invoke(read, struct{}{}, &state) {
			return "failed"
		}
	}
	commit := protocol.LogicalTurnCommit{Contexts: state.Contexts}
	for _, message := range state.Messages {
		commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
	}
	if manager {
		if !c.mutate("issues_turn_commit", "", nil, commit, nil) {
			return "failed"
		}
	} else if !c.invoke("conversation_turn_commit", commit, nil) {
		return "failed"
	}
	inputTokens, outputTokens := int64(240), int64(80)
	th.Emit(harness.Event{Kind: harness.EventRequestUsage, TurnID: turn, RequestUsage: &protocol.TokenUsageSample{Source: "sandbox-script", RequestID: turn, ThreadID: th.ID(), Revision: 1, Settlement: "final", Model: model, Input: &inputTokens, Output: &outputTokens}})
	return "completed"
}

// Review retries only a stale read/CAS, never a business mutation. Every retry
// reads the actual source versions through the same hosted tool boundary.
func (c *toolCaller) reviewCurrentIssue(issue *protocol.IssueView) bool {
	for attempt := 0; attempt < 5; attempt++ {
		if !c.invoke("issues_read", struct{}{}, issue) {
			return false
		}
		if c.reviewIssue(*issue) {
			return true
		}
		if !delay(c.ctx, 25*time.Millisecond) {
			return false
		}
	}
	return false
}

func (c *toolCaller) reviewIssue(issue protocol.IssueView) bool {
	review := protocol.IssueContextReview{ExpectedAssignmentEpoch: issue.AssignmentEpoch, ContextRevision: issue.ContextRevision, Summary: "已检查沙盒来源与当前上下文；没有额外事实。", Sources: []protocol.LogicalTurnReview{}}
	for _, source := range issue.SourceReviews {
		review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.AssociationRevision})
	}
	return c.mutate("issues_context_review", issue.IssueID, nil, review, nil)
}

func streamReply(ctx context.Context, th *testkit.FakeThread, turn, response string) bool {
	runes := []rune(response)
	for start := 0; start < len(runes); start += 12 {
		end := min(start+12, len(runes))
		if !delay(ctx, 60*time.Millisecond) {
			return false
		}
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: turn + "_reply", ItemType: harness.ItemAgentMessage, Text: string(runes[start:end])})
	}
	th.Say(turn, turn+"_reply", response)
	return true
}

func shortTitle(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return "示例任务"
	}
	return string(runes[:min(len(runes), 100)])
}

func plan(title, project string) protocol.IssuePlan {
	return protocol.IssuePlan{Title: title, Project: project, Intent: "使用独立沙盒数据验证真实界面与交互", CompletionCriteria: []string{"检查示例结果并完成独立验收"}, Stages: []protocol.IssueStage{}}
}

func taskPayload(title string, epoch int64) protocol.CreateIssueTaskPayload {
	return protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: epoch, Title: shortTitle(title), Rating: protocol.RatingP2, Spec: protocol.OwnerTaskSpec{Goal: title, Acceptance: []string{"示例报告包含进展、结果与沙盒说明"}, Deliverables: []string{"report", "files"}, MaxImplementAttempts: 1, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}
}

func delay(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
