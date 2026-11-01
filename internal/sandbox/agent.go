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
	"github.com/GhostFlying/tend/internal/serverkit"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type toolCaller struct {
	ctx          context.Context
	thread       *testkit.FakeThread
	turn         string
	call         int
	effect       int64
	log          *slog.Logger
	coordination chan struct{}
	pacing       scriptPacing
	locked       bool
}

func (c *toolCaller) invoke(name string, args, out any) bool {
	if c.ctx.Err() != nil {
		return false
	}
	c.call++
	id := fmt.Sprintf("%s_call_%d", c.turn, c.call)
	raw, _ := json.Marshal(args)
	c.thread.UseTool(c.turn, id, harness.ToolUse{Name: name, Input: string(raw), Status: protocol.ToolRunning})
	if c.call <= 2 && !delay(c.ctx, c.pacing.tool) {
		return false
	}
	if !c.locked {
		select {
		case <-c.coordination:
		case <-c.ctx.Done():
			return false
		}
		defer func() { c.coordination <- struct{}{} }()
	}
	result := c.thread.CallTool(c.ctx, c.turn, id, name, args)
	if c.ctx.Err() != nil {
		return false
	}
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

func scriptedTurn(ctx context.Context, runtime *serverkit.Runtime, th *testkit.FakeThread, turn string, input harness.TurnInput, log *slog.Logger, onReplyDelta func(context.Context, harness.TurnInput), coordination chan struct{}, pace scriptPacing) string {
	c := toolCaller{ctx: ctx, thread: th, turn: turn, log: log, coordination: coordination, pacing: pace}
	if !delay(ctx, pace.thinking) {
		return "interrupted"
	}
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
	replySource := ""
	response := "已收到。这是交互沙盒，所有消息与操作保存在独立账本中。可以试试“创建”“派发”或“问我”。也可以点击停止回复，随后继续对话。"
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
		if manager && respond {
			next := "继续澄清范围与验收条件"
			if strings.Contains(text, "总结") {
				next = "等待你复核本轮交付"
			}
			if !c.mutate("issues_update_progress", issue.IssueID, &issue.Revision, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "已核对最新要求：" + shortTitle(text), NextStep: next}, nil) {
				return "failed"
			}
			issue.Revision++
			status := protocol.IssueInProgress
			if strings.Contains(text, "总结") {
				status = protocol.IssueInReview
			}
			if issue.WorkStatus != status {
				if !c.mutate("issues_set_status", issue.IssueID, &issue.Revision, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}, nil) {
					return "failed"
				}
				issue.Revision++
			}
		}
		dispatch := strings.Contains(text, "派发") || strings.HasPrefix(strings.TrimSpace(text), "执行")
		ask := strings.Contains(text, "问我")
		switch {
		case create || dispatch && !manager:
			title := shortTitle(text)
			p := protocol.CreateIssuePayload{Plan: plan(title, "demo-web"), IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: &sourceRevision}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "sandbox-hosted/manager"}}
			if manager && issue.OwnershipPolicy == protocol.IssueOwnerPolicy {
				p.Assignee = nil
				p.Owner = &protocol.IssueOwnerRequest{CategoryID: protocol.DefaultIssueManagerCategory, AgentID: issue.Assignee.Ref, Reason: "Retain child responsibility with the current sandbox Agent"}
			}
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
			payload := taskPayload(text, issue.AssignmentEpoch)
			if issue.OwnershipPolicy == protocol.IssueOwnerPolicy {
				payload.Spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: protocol.DefaultIssueManagerCategory}, Acceptance: protocol.AgentRequirement{CategoryID: "sandbox-reviewer"}}
			}
			if strings.Contains(text, "首次失败") {
				payload.Spec.Observation = &protocol.ObservationScope{WorkItemIDs: []string{issue.IssueID}}
			}
			if !c.mutate("issues_create_task", issue.IssueID, &issue.Revision, payload, nil) {
				return "failed"
			}
			response = "任务已派发，执行 Agent 正在准备示例交付物。"
		case ask:
			if !c.mutate("attentions_create", "", nil, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "示例方案：你希望先检查哪种布局？", Options: []string{"手机布局", "桌面布局"}, SourceMessageID: source}, nil) {
				return "failed"
			}
			response = "已在收件箱提出一个决定，请选择手机或桌面布局。"
		case manager && strings.Contains(text, "范围澄清"):
			response = "范围已缩小到手机布局，桌面留到下一轮。请再确认：返回列表后应保留哪些输入，以及触控入口有哪些验收要求？"
		case manager && strings.Contains(text, "未发送草稿"):
			response = "已把未发送草稿与返回焦点记入进展。还需要明确长标题、触控入口和决定说明的阅读标准，之后再派发检查。"
		case manager && strings.Contains(text, "验收条件补充"):
			response = "验收条件已确认：长标题可读，入口不挤压标题，决定说明完整。下一步逐项核对上下文，再请你选择本轮的布局优先级。"
		case manager && strings.Contains(text, "决定已确认"):
			response = "已收到你的真实决定：手机优先，桌面以后再看。现有草稿与返回焦点要求仍保留，可以派发同一范围的检查任务。"
		case manager && strings.Contains(text, "异常退出"):
			response = "第一轮异常退出属于执行事实，不代表验收通过。失败记录会保留；请在原任务的决定中明确重试，随后由独立验收检查报告。"
		case manager && strings.Contains(text, "详细走查"):
			for n := 0; n < 3; n++ {
				if !c.invoke("issues_read", struct{}{}, nil) {
					return "failed"
				}
			}
			// Separate native message items make a real multi-page transcript while
			// a bounded number of reads avoids quadratic seed/tool bookkeeping.
			for n := 0; n < 40; n++ {
				if ctx.Err() != nil {
					return "interrupted"
				}
				th.Say(turn, fmt.Sprintf("%s_check_%d", turn, n), fmt.Sprintf("检查记录 %d：已核对手机长标题、触控入口、草稿与返回焦点的验收标准；这只是沙盒检查过程，不代表外部仓库已经修改。", n+1))
			}
			response = "已逐项读取本轮上下文。\n\n" + strings.Repeat("### 手机体验检查\n\n保留长标题、未发送草稿及返回焦点；检查决定说明和可下载报告。这里只展示沙盒脚本的走查过程，没有实际修改外部仓库。\n\n", 20)
		case manager && strings.Contains(text, "总结"):
			response = "本轮手机布局检查已完成：首次失败保留在执行历史中，按你的决定重试后交付报告，并通过独立验收。当前进展与交付均可复核，已移到待评审。"
		default:
			if manager {
				response = "已查看 Issue 的最新来源与执行进展，继续等待你的下一条消息。"
			}
		}
		replySource = source
	}
	formalReply := false
	if replySource != "" {
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m JOIN request_records r ON r.request_id=m.request_id WHERE m.message_id=? AND r.operation LIKE 'messaging.%')`, replySource).Scan(&formalReply)
		})
		if err != nil {
			return "failed"
		}
		if formalReply && !c.invoke("conversation_input_read", map[string]string{"message_id": replySource}, nil) {
			return "failed"
		}
	}
	if formalReply {
		response = strings.ReplaceAll(response, "也可以点击停止回复，随后继续对话。", "可以继续发消息讨论，或引用原问题回答。")
	}
	if respond && !input.Repair && !formalReply {
		if !streamReply(ctx, th, turn, response, pace.reply, func() {
			if onReplyDelta != nil {
				onReplyDelta(ctx, input)
			}
		}) {
			return "interrupted"
		}
		// The final frame changes content/classification versions asynchronously.
		// Wait on the real ledger, without creating polling tool items, before CAS.
		if !waitCompletedReply(ctx, runtime, th.ID(), turn, turn+"_reply") {
			return "interrupted"
		}
	}
	// Keep the metadata read/CAS sequence isolated from other scripted tools.
	// Streaming and business planning remain concurrent; native event consumers
	// must be free to persist the completed frame before this section begins.
	select {
	case <-coordination:
	case <-ctx.Done():
		return "interrupted"
	}
	c.locked = true
	defer func() { coordination <- struct{}{} }()
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
			for _, item := range message.Items {
				if item.WorkItemID != "" {
					ids = append(ids, item.WorkItemID)
				}
			}
			if createdIssue != "" && len(ids) == 0 {
				ids = append(ids, createdIssue)
			}
			if len(ids) > 0 {
				disposition = "linked"
			}
			if !c.mutate("conversation_associate_issues", message.MessageID, &message.Revision, protocol.SetIssueMessageAssociationsPayload{ContentRevision: message.ContentRevision, Disposition: disposition, IssueIDs: ids}, nil) {
				return "failed"
			}
		}
		if !c.invoke(read, struct{}{}, &state) {
			return "failed"
		}
	}
	if respond && formalReply && !input.Repair {
		publication := protocol.CoordinationReply{ReplyTo: replySource, Body: response, Purpose: "final", Disposition: "unassigned", WorkItemIDs: []string{}, Contexts: []protocol.LogicalContextReview{}, Facts: []protocol.ContextFact{}, ReviewedKinds: []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}}
		for _, message := range state.Messages {
			if message.MessageID == replySource {
				for _, item := range message.Items {
					if item.WorkItemID != "" {
						publication.WorkItemIDs = append(publication.WorkItemIDs, item.WorkItemID)
					}
				}
			}
		}
		if manager {
			publication.WorkItemIDs = []string{issue.IssueID}
		}
		if len(publication.WorkItemIDs) > 0 {
			publication.Disposition = "linked"
		}
		for _, review := range state.Contexts {
			for _, id := range publication.WorkItemIDs {
				if review.WorkItemID == id {
					publication.Contexts = append(publication.Contexts, review)
				}
			}
		}
		if !c.mutate("conversation_reply", "", nil, publication, nil) {
			return "failed"
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

func streamReply(ctx context.Context, th *testkit.FakeThread, turn, response string, duration time.Duration, firstDelta func()) bool {
	runes := []rune(response)
	interval := duration / time.Duration(max(1, (len(runes)+11)/12))
	for start := 0; start < len(runes); start += 12 {
		end := min(start+12, len(runes))
		if !delay(ctx, interval) {
			return false
		}
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: turn + "_reply", ItemType: harness.ItemAgentMessage, Text: string(runes[start:end])})
		if start == 0 && firstDelta != nil {
			firstDelta()
		}
	}
	if ctx.Err() != nil {
		return false
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

// waitCompletedReply is a read-only transport barrier, not another business tool
// or a synthetic receipt. It observes the exact native item accepted by App,
// including retained background output that is published only after settlement.
func waitCompletedReply(ctx context.Context, runtime *serverkit.Runtime, thread, turn, item string) bool {
	for {
		complete := false
		err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_agent_messages WHERE thread_id=? AND turn_id=? AND item_id=? AND status='completed' UNION ALL SELECT 1 FROM background_replies WHERE thread_id=? AND turn_id=? AND item_id=? AND status='completed')`, thread, turn, item, thread, turn, item).Scan(&complete)
		})
		if err != nil {
			return false
		}
		if complete {
			return true
		}
		if !delay(ctx, 100*time.Millisecond) {
			return false
		}
	}
}

// Script pacing is local to each sandbox instance, never production execution.
type scriptPacing struct {
	thinking   time.Duration
	tool       time.Duration
	reply      time.Duration
	workerStep time.Duration
}

func pacing(fast bool) scriptPacing {
	if fast {
		return scriptPacing{thinking: time.Millisecond, tool: time.Millisecond, reply: 120 * time.Millisecond, workerStep: time.Millisecond}
	}
	return scriptPacing{thinking: 2 * time.Second, tool: 2 * time.Second, reply: 2 * time.Second, workerStep: 5 * time.Second}
}
