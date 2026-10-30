package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func issueDetailMetadata(ctx context.Context, q store.Querier, out *protocol.IssueView) error {
	p, err := store.IssueProgress(ctx, q, out.IssueID, out.Revision)
	if err != nil {
		return err
	}
	out.Progress = p
	if out.Historical {
		return nil
	}
	binding := &protocol.IssueConversationBinding{Reason: "unavailable"}
	err = q.QueryRowContext(ctx, `SELECT conversation_id FROM agent_conversations WHERE work_item_id=?`, out.IssueID).Scan(&binding.ConversationID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	switch {
	case out.State == protocol.IssueClosed || out.State == protocol.IssueCancelled || out.State == protocol.IssueCancelling:
		binding.Reason = "ended"
	case out.AssignmentState != "active":
		binding.Reason = "handoff"
	case out.Manager != nil && out.Manager.HostID == "" && !out.Manager.State.Terminal():
		binding.Reason = "manager_pending"
	case out.Assignee.Kind == protocol.IssueAssigneeOwner:
		binding.Reason = "owner"
	case binding.ConversationID != "":
		binding.CanSend = true
		binding.Reason = ""
	}
	out.Conversation = binding
	for id := out.ParentIssueID; id != ""; {
		parent, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		out.Ancestors = append(out.Ancestors, protocol.IssueAncestor{IssueID: parent.IssueID, Title: parent.Plan.Title, State: parent.State})
		id = parent.ParentIssueID
	}
	slices.Reverse(out.Ancestors)
	return nil
}

// IssueResults reads bounded artifact summaries, never every task's native history.
func IssueResults(ctx context.Context, q store.Querier, id, after string) (protocol.IssueResultPage, error) {
	out := protocol.IssueResultPage{Items: []protocol.IssueResult{}}
	if _, err := store.GetCanonicalIssue(ctx, q, id); err != nil {
		return out, err
	}
	before := int64(0)
	if after != "" {
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil || n < 1 {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid result cursor")
		}
		before = n
	}
	rows, err := q.QueryContext(ctx, `SELECT a.rowid,a.artifact_id FROM artifacts a JOIN task_work_item_owners o ON o.task_id=a.task_id JOIN tasks t ON t.task_id=a.task_id WHERE o.work_item_id=? AND t.kind<>'issue_manager' AND (?=0 OR a.rowid<?) ORDER BY a.rowid DESC LIMIT 21`, id, before, before)
	if err != nil {
		return out, err
	}
	type ref struct {
		pos int64
		id  string
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.pos, &r.id); err != nil {
			rows.Close()
			return out, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(refs) > 20 {
		out.More = true
		refs = refs[:20]
		out.Next = strconv.FormatInt(refs[len(refs)-1].pos, 10)
	}
	for _, r := range refs {
		result, err := issueResult(ctx, q, r.id)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, result)
	}
	return out, nil
}

func issueResult(ctx context.Context, q store.Querier, id string) (protocol.IssueResult, error) {
	var out protocol.IssueResult
	detail, err := Artifact(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.ArtifactDetail = detail
	artifact, err := store.GetArtifact(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.CreatedAt = artifact.CreatedAt
	out.RunID = artifact.RunID
	err = q.QueryRowContext(ctx, `SELECT t.title,p.target_spec_version,r.generation FROM runs r JOIN plan_actions p USING(action_id) JOIN tasks t ON t.task_id=r.task_id WHERE r.run_id=?`, artifact.RunID).Scan(&out.Title, &out.SpecVersion, &out.Generation)
	if err != nil {
		return out, err
	}
	out.Acceptances = slices.DeleteFunc(out.Acceptances, func(a protocol.AcceptanceView) bool { return a.SpecVersion != out.SpecVersion })
	// Reports remain previewable, with the complete artifact available on demand.
	var payload protocol.ArtifactPayload
	if err := json.Unmarshal(out.Artifact.Ref, &payload); err == nil {
		payload.Mounts = nil
		if payload.Report != nil {
			copyReport := *payload.Report
			body := []rune(copyReport.Body)
			if len(body) > 1200 {
				copyReport.Body = string(body[:1200]) + "\n\n…"
			}
			copyReport.Evidence = nil
			copyReport.Uncertainties = nil
			payload.Report = &copyReport
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return out, err
		}
		out.Artifact.Ref = raw
	}
	return out, nil
}

var issueActivityTitles = map[string]string{
	"issue.created": "创建了 Issue", "issue.updated": "更新了目标与定义", "issue.progress_updated": "更新了当前进展",
	"issue.manager_claimed": "负责人已接手", "issue.manager_fenced": "开始交接负责人", "issue.manager_migration_cancelled": "撤回了负责人委派", "task.manager_requested": "发布了负责人任务",
	"issue.assigned": "完成了负责人交接", "issue.assignment_fenced": "开始交接负责人", "issue.paused": "暂停了 Issue", "issue.resumed": "恢复了 Issue",
	"issue.status_changed": "更新了工作状态",
	"issue.closed":         "关闭了 Issue", "issue.cancelled": "取消了 Issue", "issue.cancelling": "正在取消 Issue", "issue.reopened": "重新打开了 Issue", "issue.reparented": "调整了上级 Issue",
	"run.unknown": "执行结果未知，待核实", "run.lost": "执行连接已丢失，待核实", "run.recovery": "执行正在恢复", "run.failed": "执行失败", "run.reconciled": "执行结果已对账", "task.cancelled": "取消了任务", "task.taken_over": "任务已由用户接管",
	"attention.changed": "更新了待处理事项",
}

// IssueActivity merges existing transcript blocks and selected immutable facts
// on their creation event. Streaming updates never move a block's position.
func IssueActivity(ctx context.Context, q store.Querier, id string, before int64) (protocol.IssueActivityPage, error) {
	out := protocol.IssueActivityPage{Items: []protocol.IssueActivityEntry{}}
	issue, err := store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.EventSeq, err = store.LatestEventSeq(ctx, q)
	if err != nil {
		return out, err
	}
	if before < 0 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid activity cursor")
	}
	conversation := ""
	err = q.QueryRowContext(ctx, `SELECT conversation_id FROM agent_conversations WHERE work_item_id=?`, id).Scan(&conversation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	kinds := []string{"artifact.submitted"}
	for kind := range issueActivityTitles {
		kinds = append(kinds, kind)
	}
	rawKinds, _ := json.Marshal(kinds)
	// Each union branch has a durable global event position. No timestamp-only
	// cursor or synthesized child transcript is used.
	rows, err := q.QueryContext(ctx, `WITH primary_tasks AS (SELECT o.task_id FROM task_work_item_owners o JOIN tasks t USING(task_id) WHERE o.work_item_id=? AND t.kind<>'issue_manager'),
 manager_tasks AS (SELECT task_id FROM issue_manager_tasks WHERE issue_id=?),
 entries AS (
 SELECT MIN(e.event_seq) AS position,'conversation' AS kind,m.message_id AS object_id,m.created_at AS created_at
 FROM conversation_blocks b JOIN conversation_messages m ON m.message_id=b.message_id
 JOIN events e ON e.object_type='message' AND e.object_id=m.message_id AND e.kind='conversation.message'
 WHERE m.conversation_id=? AND b.notice_only=0
 AND NOT EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=m.message_id AND c.withdrawn_at<>0)
 AND NOT EXISTS(SELECT 1 FROM background_batches bg WHERE bg.message_id=m.message_id AND bg.status='completed' AND bg.checkin_json<>'')
 GROUP BY m.message_id
 UNION ALL
 SELECT e.event_seq,e.kind,e.object_id,e.created_at FROM events e
 WHERE e.kind IN (SELECT value FROM json_each(?)) AND (
 (e.object_type='issue' AND e.object_id=?)
 OR (e.kind='task.manager_requested' AND e.object_id IN (SELECT task_id FROM manager_tasks))
 OR (e.object_id IN (SELECT task_id FROM primary_tasks))
 OR (e.object_type='run' AND e.object_id IN (SELECT run_id FROM runs WHERE task_id IN (SELECT task_id FROM primary_tasks)))
 OR (e.object_type='artifact' AND e.object_id IN (SELECT artifact_id FROM artifacts WHERE task_id IN (SELECT task_id FROM primary_tasks)))
 OR (e.object_type='attention' AND e.object_id IN (SELECT attention_id FROM attentions WHERE issue_id=?))
 OR (e.object_type='issue' AND e.object_id IN (SELECT issue_id FROM issue_controls WHERE parent_issue_id=?) AND
 (e.kind IN ('issue.closed','issue.cancelled','issue.assigned','issue.manager_claimed','issue.manager_migration_cancelled') OR (e.kind='issue.status_changed' AND json_extract(e.body_json,'$.state') IN ('closed','cancelled'))))
 ))
 SELECT position,kind,object_id,created_at FROM entries WHERE position<=? AND (?=0 OR position<?) ORDER BY position DESC LIMIT 21`, id, id, conversation, string(rawKinds), id, id, id, out.EventSeq, before, before)
	if err != nil {
		return out, fmt.Errorf("views: issue activity: %w", err)
	}
	type row struct {
		seq      int64
		kind, id string
		at       int64
	}
	var refs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.kind, &r.id, &r.at); err != nil {
			rows.Close()
			return out, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(refs) > 20 {
		out.More = true
		refs = refs[:20]
		out.NextBefore = refs[len(refs)-1].seq
	}
	for _, r := range refs {
		entry := protocol.IssueActivityEntry{ID: fmt.Sprintf("event:%d", r.seq), Position: r.seq, Kind: r.kind, CreatedAt: r.at, Author: "system", Title: issueActivityTitles[r.kind]}
		if r.kind == "conversation" {
			block, err := TranscriptBlock(ctx, q, r.id, 0, 50)
			if err != nil {
				return out, err
			}
			projected, err := OwnerIssueTranscriptBlock(ctx, q, block)
			if err != nil {
				return out, err
			}
			entry.ID = "block:" + r.id
			entry.Block = &projected
			entry.Author = "负责人"
			var frozen string
			err = q.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(json_extract(h.private_snapshot,'$.definition.display_name'),''),json_extract(h.private_snapshot,'$.identity.execution_environment_id')||'/'||json_extract(h.private_snapshot,'$.identity.local_id'),'负责人') FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE t.message_id=? ORDER BY h.created_at LIMIT 1`, r.id).Scan(&frozen)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			if frozen != "" {
				entry.Author = frozen
			}
		} else {
			var body string
			if err := q.QueryRowContext(ctx, `SELECT body_json FROM events WHERE event_seq=?`, r.seq).Scan(&body); err != nil {
				return out, err
			}
			var facts struct {
				Status     string `json:"status"`
				State      string `json:"state"`
				ArtifactID string `json:"artifact_id"`
				TaskID     string `json:"task_id"`
			}
			if err := json.Unmarshal([]byte(body), &facts); err != nil {
				return out, err
			}
			if r.kind == "issue.status_changed" {
				switch facts.State {
				case "closed":
					entry.Title = "关闭了 Issue"
				case "cancelled":
					entry.Title = "取消了 Issue"
				}
			}
			if r.id == issue.IssueID {
				entry.IssueID = id
			} else {
				var childTitle string
				e := q.QueryRowContext(ctx, `SELECT json_extract(w.body_json,'$.title') FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE c.issue_id=? AND c.parent_issue_id=?`, r.id, id).Scan(&childTitle)
				if e == nil {
					entry.IssueID = r.id
					entry.Title = childTitle + " · " + entry.Title
				} else if !errors.Is(e, sql.ErrNoRows) {
					return out, e
				}
			}
			if r.kind == "attention.changed" {
				var snapshot string
				err := q.QueryRowContext(ctx, `SELECT ar.body_json FROM attention_revisions ar JOIN events e ON e.object_id=ar.attention_id AND e.object_version=ar.revision WHERE e.event_seq=?`, r.seq).Scan(&snapshot)
				if err != nil {
					return out, err
				}
				var attention protocol.Attention
				if err := json.Unmarshal([]byte(snapshot), &attention); err != nil {
					return out, err
				}
				switch facts.Status {
				case "open":
					entry.Title = "新增或更新了待处理事项"
				case "responded":
					entry.Title = "已回应待处理事项"
				case "resolved":
					entry.Title = "待处理事项已解决"
				case "cancelled", "withdrawn":
					entry.Title = "待处理事项已取消"
				}
				if attention.Kind == "information" {
					entry.Title = "通知"
				}
				entry.Title += "：" + attention.Title
			}
			entry.TaskID = facts.TaskID
			entry.ArtifactID = facts.ArtifactID
			if entry.TaskID == "" {
				_ = q.QueryRowContext(ctx, `SELECT task_id FROM tasks WHERE task_id=? UNION ALL SELECT task_id FROM runs WHERE run_id=? LIMIT 1`, r.id, r.id).Scan(&entry.TaskID)
			}
			if r.kind == "artifact.submitted" {
				entry.Title = "实施已交付"
				if entry.ArtifactID == "" {
					entry.ArtifactID = r.id
				}
				entry.ID = "result:" + entry.ArtifactID
			}
		}
		out.Items = append(out.Items, entry)
	}
	slices.Reverse(out.Items)
	return out, nil
}
