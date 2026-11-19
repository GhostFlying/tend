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

func issueDetailMetadata(ctx context.Context, q store.Querier, out *protocol.IssueView, facts executionFacts) error {
	out.EffectiveWorkStatus = out.BoardStatus()
	p, err := issueProgressProjection(ctx, q, out.Issue, !out.Historical)
	if err != nil {
		return err
	}
	out.Progress = p
	if out.State == protocol.IssueCancelling || out.State == protocol.IssueCancelled {
		// Read the intent that began this cancellation, including a direct settled
		// cancellation. Later settlement and older responsibility cycles have their own reasons.
		err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(c.reason_json,'$.reason'),'')
 FROM issue_control_revisions c JOIN issue_control_revisions prev
 ON prev.issue_id=c.issue_id AND prev.revision=c.revision-1
 WHERE c.issue_id=? AND c.revision<=?
 AND json_extract(c.snapshot_json,'$.control.state') IN ('cancelling','cancelled')
 AND json_extract(prev.snapshot_json,'$.control.state') NOT IN ('cancelling','cancelled')
 ORDER BY c.revision DESC LIMIT 1`, out.IssueID, out.Revision).Scan(&out.CancellationReason)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("views: read issue cancellation reason: %w", err)
		}
	}
	if out.Historical {
		return nil
	}
	out.QueueReason, out.Pause, err = facts.issueAdmissionProgress(ctx, q, out.Issue)
	if err != nil {
		return err
	}
	out.Children, err = issueChildren(ctx, q, out.IssueID, facts)
	if err != nil {
		return err
	}
	binding := &protocol.IssueConversationBinding{Reason: "unavailable"}
	err = q.QueryRowContext(ctx, `SELECT conversation_id FROM agent_conversations WHERE work_item_id=?`, out.IssueID).Scan(&binding.ConversationID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	switch {
	case out.WorkStatus == protocol.IssueBacklog:
		binding.Reason = "backlog"
		binding.CanSend = binding.ConversationID != ""
	case out.State == protocol.IssueClosed || out.State == protocol.IssueCancelled || out.State == protocol.IssueCancelling:
		binding.Reason = "ended"
	case out.AssignmentState != "active":
		binding.Reason = "handoff"
	case out.Manager != nil && out.Manager.HostID == "" && !out.Manager.State.Terminal():
		binding.Reason = "manager_pending"
		binding.CanSend = binding.ConversationID != ""
	case out.OwnerSelection != nil && out.Manager == nil:
		binding.Reason = "owner_pending"
		binding.CanSend = binding.ConversationID != ""
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
	rows, err := q.QueryContext(ctx, `SELECT a.rowid,a.artifact_id FROM artifacts a JOIN task_work_item_owners o ON o.task_id=a.task_id JOIN tasks t ON t.task_id=a.task_id WHERE o.work_item_id=? AND (t.kind<>'issue_manager' OR EXISTS(SELECT 1 FROM issue_owner_results r WHERE r.artifact_id=a.artifact_id)) AND (?=0 OR a.rowid<?) ORDER BY a.rowid DESC LIMIT 21`, id, before, before)
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
	"run.unknown": "执行结果未知，待核实", "run.lost": "执行连接已丢失，待核实", "run.recovery": "更新了执行恢复状态", "run.failed": "执行失败", "run.reconciled": "执行结果已对账", "task.cancelled": "取消了任务", "task.taken_over": "任务已由用户接管",
	"attention.changed":             "更新了待处理事项",
	"issue.delivery_review_changed": "更新了 PR 审阅",
	"work_item.delivery_changed":    "更新了交付观察",
}

type IssueActivityQuery struct {
	Readable bool
	Before   int64
	Since    int64
	Through  int64
	From     int64
}

// IssueActivity merges existing transcript blocks and selected immutable facts
// on their creation event. Streaming updates never move a block's position.
func IssueActivity(ctx context.Context, q store.Querier, id string, before int64) (protocol.IssueActivityPage, error) {
	return ReadIssueActivity(ctx, q, id, IssueActivityQuery{Before: before})
}

func ReadIssueActivity(ctx context.Context, q store.Querier, id string, query IssueActivityQuery) (protocol.IssueActivityPage, error) {
	out := protocol.IssueActivityPage{Items: []protocol.IssueActivityEntry{}, Readable: query.Readable}
	issue, err := store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.EventSeq, err = store.LatestEventSeq(ctx, q)
	if err != nil {
		return out, err
	}
	if query.Before < 0 || query.Since < 0 || query.From < 0 || query.Through < 0 ||
		(query.Since == 0 && (query.From != 0 || query.Through != 0)) ||
		(query.Since > 0 && !query.Readable) || query.Since > out.EventSeq ||
		(query.Through > 0 && (query.Through < query.Since || query.Through > out.EventSeq)) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid activity cursor")
	}
	if query.Through > 0 {
		out.EventSeq = query.Through
	}
	if query.Since > 0 && query.Since == out.EventSeq {
		return out, nil
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
	limit := 20
	if query.Readable {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, `WITH
 query(readable,since,through) AS (VALUES(?,?,?)),
 primary_tasks AS (SELECT o.task_id FROM task_work_item_owners o JOIN tasks t USING(task_id) WHERE o.work_item_id=? AND t.kind<>'issue_manager'),
 manager_tasks AS (SELECT task_id FROM issue_manager_tasks WHERE issue_id=?),
 changed_message_ids(id) AS (
  SELECT CASE WHEN e.object_type='message' THEN e.object_id ELSE json_extract(e.body_json,'$.message_id') END
  FROM query, events e
  WHERE query.since>0 AND e.event_seq>query.since AND e.event_seq<=query.through
   AND (e.object_type='message' OR e.object_type='work_item_choice')
  UNION
  SELECT r.object_id FROM query, refresh_notifications r
  WHERE query.since>0 AND r.event_seq>query.since AND r.event_seq<=query.through
   AND r.kind='conversation.process_changed' AND r.object_type='message'
 ),
 changed_messages AS MATERIALIZED (
  SELECT m.message_id,m.reply_to FROM changed_message_ids d JOIN conversation_messages m ON m.message_id=d.id
 ),
 changed_blocks(id) AS MATERIALIZED (
  SELECT message_id FROM changed_messages
  UNION SELECT reply_to FROM changed_messages WHERE reply_to<>''
  UNION SELECT s.source_id FROM changed_messages m JOIN conversation_reply_sources s ON s.reply_id=m.message_id
 ),
 conversation_entries AS (
 SELECT e.event_seq AS position, m.message_id AS object_id, m.created_at,
  (EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=m.message_id AND c.withdrawn_at<>0)
   OR EXISTS(SELECT 1 FROM background_batches bg WHERE bg.message_id=m.message_id AND bg.status='completed' AND bg.checkin_json<>'')) AS removed
 FROM query, conversation_messages m JOIN conversation_blocks b ON b.message_id=m.message_id AND b.notice_only=0
 JOIN events e ON e.event_seq=(SELECT MIN(created.event_seq) FROM events created
  WHERE created.object_type='message' AND created.object_id=m.message_id AND created.kind='conversation.message')
 WHERE m.conversation_id=? AND (NOT query.readable OR m.kind<>'tool_use')
 AND (query.since=0 OR e.event_seq>query.since OR m.message_id IN (SELECT id FROM changed_blocks))
 ),
 entries AS (
 SELECT position, CASE WHEN removed THEN 'removed' ELSE 'conversation' END AS kind, object_id, created_at
 FROM conversation_entries WHERE NOT removed OR (SELECT since FROM query)>0
 UNION ALL
 SELECT e.event_seq,e.kind,e.object_id,e.created_at FROM events e
 WHERE e.event_seq>(SELECT since FROM query) AND e.kind IN (SELECT value FROM json_each(?)) AND (
 (e.object_type='issue' AND e.object_id=?)
 OR (e.kind='work_item.delivery_changed' AND e.object_type='work_item' AND e.object_id=?)
 OR (e.kind='task.manager_requested' AND e.object_id IN (SELECT task_id FROM manager_tasks))
 OR (e.object_id IN (SELECT task_id FROM primary_tasks))
 OR (e.object_type='run' AND e.object_id IN (SELECT run_id FROM runs WHERE task_id IN (SELECT task_id FROM primary_tasks)))
 OR (e.object_type='artifact' AND e.object_id IN (SELECT artifact_id FROM artifacts WHERE task_id IN (SELECT task_id FROM primary_tasks)))
 OR (e.object_type='attention' AND e.object_id IN (SELECT attention_id FROM attentions WHERE issue_id=?))
 OR (e.object_type='issue' AND EXISTS (SELECT 1 FROM issue_control_revisions c WHERE c.issue_id=e.object_id AND c.revision=e.object_version AND json_extract(c.snapshot_json,'$.control.parent_issue_id')=?) AND
 (e.kind IN ('issue.closed','issue.cancelled','issue.assigned','issue.manager_claimed','issue.manager_migration_cancelled') OR (e.kind='issue.status_changed' AND json_extract(e.body_json,'$.state') IN ('closed','cancelled'))))
 )
 UNION ALL
 SELECT e.event_seq,e.kind,e.object_id,e.created_at
 FROM (SELECT DISTINCT s.event_id FROM background_batches b JOIN background_sources s USING(batch_id) WHERE b.work_item_id=?) linked
 CROSS JOIN events e ON e.event_id=linked.event_id
 WHERE e.object_type='task' AND e.kind='task.completed'
 AND ((SELECT since FROM query)=0 OR e.event_seq>(SELECT since FROM query)
  OR EXISTS(SELECT 1 FROM background_sources s JOIN background_batches b USING(batch_id)
    WHERE s.event_id=linked.event_id AND b.work_item_id=? AND b.message_id IN (SELECT id FROM changed_blocks)))
 UNION ALL
 SELECT e.event_seq,e.kind,h.delivery_id,e.created_at FROM issue_delivery_candidate_history h
 JOIN events e ON e.event_seq=h.event_seq WHERE h.issue_id=? AND e.event_seq>(SELECT since FROM query)
 )
 SELECT position,kind,object_id,created_at FROM entries
 WHERE position<=? AND position>=? AND (?=0 OR position<?) ORDER BY position DESC LIMIT ?`,
		query.Readable, query.Since, out.EventSeq, id, id, conversation, string(rawKinds), id, id, id, id, id, id, id,
		out.EventSeq, query.From, query.Before, query.Before, limit+1)
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
	if len(refs) > limit {
		out.More = true
		refs = refs[:limit]
	}
	const readableBudget = 512 * 1024
	size := 0
	var lastPosition int64
	for _, r := range refs {
		if r.kind == "removed" {
			out.Removed = append(out.Removed, "block:"+r.id)
			lastPosition = r.seq
			continue
		}
		entry := protocol.IssueActivityEntry{ID: fmt.Sprintf("event:%d", r.seq), Position: r.seq, Kind: r.kind, CreatedAt: r.at, Author: "tend", Title: issueActivityTitles[r.kind]}
		if r.kind == "conversation" {
			block, err := transcriptBlock(ctx, q, r.id, 0, 50, query.Readable)
			if err != nil {
				return out, err
			}
			projected, err := ownerIssueTranscriptBlock(ctx, q, block, 0, query.Readable)
			if err != nil {
				return out, err
			}
			entry.ID = "block:" + r.id
			entry.Block = &projected
			if steer := projected.Trigger.IssueOwnerSteer; steer != nil {
				entry.Author = issueSteerSender(steer.Sender)
			} else {
				entry.Author, err = issueConversationAuthor(ctx, q, r.id)
				if err != nil {
					return out, err
				}
			}
		} else {
			var body, rawActor string
			var revision int64
			if err := q.QueryRowContext(ctx, `SELECT body_json,COALESCE(actor_json,''),COALESCE(object_version,0) FROM events WHERE event_seq=?`, r.seq).Scan(&body, &rawActor, &revision); err != nil {
				return out, err
			}
			var actor protocol.Principal
			if rawActor != "" {
				if err := json.Unmarshal([]byte(rawActor), &actor); err != nil {
					return out, err
				}
			}
			entry.Author, err = issueActivityAuthor(ctx, q, actor, r.at)
			if err != nil {
				return out, err
			}
			if r.kind == "task.completed" {
				entry.IssueID, entry.TaskID, entry.Title = id, r.id, "任务结果已返回"
			} else {
				var facts struct {
					Delivery   *protocol.IssueDeliveryView `json:"delivery_progress"`
					Kind       string                      `json:"kind"`
					Status     string                      `json:"status"`
					State      string                      `json:"state"`
					ArtifactID string                      `json:"artifact_id"`
					TaskID     string                      `json:"task_id"`
				}
				if err := json.Unmarshal([]byte(body), &facts); err != nil {
					return out, err
				}
				switch r.kind {
				case "work_item.delivery_changed":
					entry.Delivery, entry.Title = facts.Delivery, "记录了交付候选观察"
				case "work_item.delivery_candidate_retired":
					if facts.Delivery != nil {
						facts.Delivery.Delivery.DeliveryID = r.id
						facts.Delivery.Observation.IssueID = id
						facts.Delivery.Observation.DeliveryID = r.id
					}
					entry.Delivery, entry.Title = facts.Delivery, "保留了被替换的候选观察"
				}
				if r.kind == "issue.progress_updated" {
					p, err := store.IssueProgress(ctx, q, r.id, revision)
					if err != nil {
						return out, err
					}
					if p != nil && p.IssueRevision == revision {
						entry.Progress = p
					}
				}
				if r.kind == "issue.manager_claimed" {
					var task string
					if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(c.reason_json,'$.manager_task'),'') FROM events e JOIN issue_control_revisions c ON c.issue_id=e.object_id AND c.revision=e.object_version WHERE e.event_seq=?`, r.seq).Scan(&task); err != nil {
						return out, err
					}
					if task != "" {
						var run string
						err := q.QueryRowContext(ctx, `SELECT run_id FROM runs WHERE task_id=? AND role='issue_manager' ORDER BY created_at,run_id LIMIT 1`, task).Scan(&run)
						if err != nil && !errors.Is(err, sql.ErrNoRows) {
							return out, err
						}
						if run != "" {
							name, e := runActivityAuthor(ctx, q, run, "Agent 身份未记录")
							if e != nil {
								return out, e
							}
							entry.Title = name + " 已接手"
						}
					}
				}
				if r.kind == "run.recovery" {
					entry.Title = recoveryActivityTitle(facts.Kind)
				}
				if r.kind == "issue.status_changed" {
					switch facts.State {
					case "closed":
						entry.Title = "关闭了 Issue"
					case "cancelled":
						entry.Title = "取消了 Issue"
					case "active":
						if actor.Kind == protocol.PrincipalMainAgent {
							entry.Admission, err = mainIssueAdmission(ctx, q, r.id, revision)
							if err != nil {
								return out, err
							}
							if entry.Admission {
								entry.Title = "将工作状态从「待安排」移到「待推进」"
							}
						}
					}
				}
				if r.id == issue.IssueID {
					entry.IssueID = id
				} else {
					var childTitle string
					e := q.QueryRowContext(ctx, `SELECT json_extract(w.body_json,'$.title') FROM events e JOIN issue_control_revisions c ON c.issue_id=e.object_id AND c.revision=e.object_version JOIN work_item_revisions w ON w.work_item_id=c.issue_id AND w.revision=c.revision WHERE e.event_seq=? AND e.object_type='issue' AND json_extract(c.snapshot_json,'$.control.parent_issue_id')=?`, r.seq, id).Scan(&childTitle)
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
					entry.Title = "已交付成果"
					if entry.ArtifactID == "" {
						entry.ArtifactID = r.id
					}
					entry.ID = "result:" + entry.ArtifactID
				}
			}
		}
		if query.Readable {
			raw, err := json.Marshal(entry)
			if err != nil {
				return out, err
			}
			if len(out.Items) > 0 && size+len(raw)+1 > readableBudget {
				out.More = true
				break
			}
			size += len(raw) + 1
		}
		out.Items = append(out.Items, entry)
		lastPosition = r.seq
	}
	if out.More {
		out.NextBefore = lastPosition
	}
	slices.Reverse(out.Items)
	return out, nil
}

// Recovery facts describe the observed phase, not an inferred run outcome.
func recoveryActivityTitle(kind string) string {
	switch kind {
	case "worker.root_background_pending":
		return "执行后台工作尚未结束"
	case "worker.root_observation_resolved":
		return "执行状态已核实"
	case "worker.root_observation_unknown":
		return "执行状态未知，待核实"
	case "worker.turn_failed":
		return "执行回合失败"
	case "worker.recovery_pending":
		return "执行等待恢复"
	case "worker.recovery_started":
		return "执行正在恢复"
	case "worker.recovery_completed":
		return "执行恢复已完成"
	case "worker.recovery_failed":
		return "执行恢复失败"
	case "worker.recovery_handed_back":
		return "执行控制已交还"
	case "worker.startup_failed":
		return "执行启动失败"
	case "worker.startup_retry_pending":
		return "执行等待重试启动"
	case "worker.startup_started":
		return "执行正在启动"
	default:
		return issueActivityTitles["run.recovery"]
	}
}

// Attribution comes from the immutable request principal, not the current Issue
// assignee. Frozen selections keep old activity correct after a handoff.
// Match only admission, using the original control revisions even after later moves.
func mainIssueAdmission(ctx context.Context, q store.Querier, id string, revision int64) (bool, error) {
	var beforeJSON, afterJSON string
	err := q.QueryRowContext(ctx, `SELECT json_extract(prev.snapshot_json,'$.control'),json_extract(cur.snapshot_json,'$.control')
 FROM issue_control_revisions cur JOIN issue_control_revisions prev
 ON prev.issue_id=cur.issue_id AND prev.revision=cur.revision-1
 WHERE cur.issue_id=? AND cur.revision=?`, id, revision).Scan(&beforeJSON, &afterJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("views: read issue admission revisions: %w", err)
	}
	var before, after protocol.IssueControl
	if err := json.Unmarshal([]byte(beforeJSON), &before); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(afterJSON), &after); err != nil {
		return false, err
	}
	return before.WorkStatus == protocol.IssueBacklog && after.WorkStatus == protocol.IssueTodo &&
		before.State == protocol.IssueActive && after.State == protocol.IssueActive &&
		before.Assignee == after.Assignee && before.AssignmentEpoch == after.AssignmentEpoch &&
		before.AssignmentState == after.AssignmentState, nil
}

func issueActivityAuthor(ctx context.Context, q store.Querier, actor protocol.Principal, at int64) (string, error) {
	switch actor.Kind {
	case protocol.PrincipalOwner:
		return "你", nil
	case protocol.PrincipalMainAgent:
		return "主 Agent", nil
	case protocol.PrincipalBackgroundAgent:
		return activityDisplayName(ctx, q, "负责人 Agent", `SELECT COALESCE(json_extract(h.private_snapshot,'$.definition.display_name'),'') FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id JOIN agent_conversations c ON c.conversation_id=h.conversation_id WHERE h.conversation_id=? AND c.work_item_id IS NOT NULL AND (?='' OR t.thread_id=?) AND h.created_at<=? ORDER BY h.created_at DESC,h.execution_id DESC LIMIT 1`, actor.ConversationID, actor.Thread, actor.Thread, at)
	case protocol.PrincipalRun:
		fallback := "执行 Agent"
		if actor.Role == protocol.RoleIssueManager {
			fallback = "负责人 Agent"
		}
		return runActivityAuthor(ctx, q, actor.Subject, fallback)
	case protocol.PrincipalRunner:
		// Hosts have no display-name field in the current catalog projection.
		if actor.Subject != "" {
			return actor.Subject, nil
		}
		return "主机身份未记录", nil
	default:
		return "tend", nil
	}
}

func activityDisplayName(ctx context.Context, q store.Querier, fallback, query string, args ...any) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, query, args...).Scan(&name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if name == "" {
		name = fallback
	}
	return name, nil
}
