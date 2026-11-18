package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Membership is explicit. Other Issues may be linked to the same message for
// context without participating in its completion report.
func completionReportChange(ctx context.Context, q store.Querier, old protocol.MessageAssociations, linked map[string]bool, members *[]string) (bool, error) {
	if members == nil {
		return false, nil
	}
	ids := slices.Clone(*members)
	slices.Sort(ids)
	for i, id := range ids {
		if !linked[id] || (i > 0 && id == ids[i-1]) {
			return false, protocol.Errorf(protocol.CodeInvalidRequest, "completion report needs distinct linked Issue IDs")
		}
	}
	*members = ids
	if old.CompletionReport != nil {
		if slices.Equal(old.CompletionReport.IssueIDs, ids) {
			return false, nil
		}
		if old.CompletionReport.PublishedMessageID != "" {
			return false, protocol.Errorf(protocol.CodeInvalidTransition, "this request already has its completion report; use the new owner's request for a new promise")
		}
	} else if len(ids) == 0 {
		return false, nil
	}
	var authorized bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m
		WHERE m.message_id=? AND m.author='owner' AND NOT EXISTS(SELECT 1 FROM messaging_messages amended WHERE amended.correction_of=m.message_id) AND NOT EXISTS
		(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=m.message_id AND c.withdrawn_at<>0))`, old.MessageID).Scan(&authorized); err != nil {
		return false, err
	}
	if !authorized {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "use the original current owner message for its completion report")
	}
	return true, nil
}

func setCompletionReport(ctx context.Context, x *Exec, tx *store.Tx, source string, revision int64, members []string) error {
	if len(members) == 0 {
		_, err := tx.Exec(ctx, `DELETE FROM issue_completion_reports WHERE source_message_id=? AND published_message_id IS NULL`, source)
		return err
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO issue_completion_reports(source_message_id,content_revision,issue_ids_json)
		VALUES(?,?,?) ON CONFLICT(source_message_id) DO UPDATE SET content_revision=excluded.content_revision,issue_ids_json=excluded.issue_ids_json`, source, revision, string(raw)); err != nil {
		return err
	}
	// Registration and a simultaneous final closure serialize on this writer.
	return publishCompletedIssues(ctx, x, tx, source)
}

func completeMessageReports(ctx context.Context, x *Exec, tx *store.Tx, issue string) error {
	sources, err := store.FollowupIDs(ctx, tx.Q(), `SELECT a.message_id FROM message_work_items a JOIN issue_completion_reports r
		ON r.source_message_id=a.message_id WHERE a.work_item_id=? AND a.active=1
		AND r.published_message_id IS NULL AND a.work_item_id IN (SELECT value FROM json_each(r.issue_ids_json)) ORDER BY a.message_id`, issue)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if err := publishCompletedIssues(ctx, x, tx, source); err != nil {
			return err
		}
	}
	return nil
}

func publishCompletedIssues(ctx context.Context, x *Exec, tx *store.Tx, source string) error {
	// Read the actual completed endpoint of every frozen member. A cancelled,
	// transferred or declined Issue also has a terminal state and cannot count.
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT j.value FROM issue_completion_reports r JOIN json_each(r.issue_ids_json) j
		JOIN message_dispositions d ON d.message_id=r.source_message_id AND d.content_revision=r.content_revision
		WHERE r.source_message_id=? AND r.published_message_id IS NULL
		AND NOT EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=r.source_message_id AND c.withdrawn_at<>0)
		AND NOT EXISTS(SELECT 1 FROM json_each(r.issue_ids_json) m
			LEFT JOIN issue_controls c ON c.issue_id=m.value LEFT JOIN work_items w ON w.work_item_id=m.value
			WHERE c.state IS NOT 'closed' OR json_extract(w.body_json,'$.closure.kind') IS NOT 'completed')
		ORDER BY j.value`, source)
	if err != nil || len(ids) == 0 {
		return err
	}
	var body strings.Builder
	const introduction = "约定的工作已全部完成。"
	body.WriteString(introduction)
	// Even a long agreement yields one text part on every configured channel.
	// UTF-8 byte length overestimates the UTF-16 units in the introduction.
	memberBudget := (feishuTextPartLimit-len(introduction))/len(ids) - 2
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		// Linked Issues retain the full result and original supporting evidence.
		text := reportExcerpt(issue.Plan.Title, memberBudget/3) + "\n" + issue.Closure.Reason
		if evidence := issue.Closure.Evidence; len(evidence) > 0 && evidence[0].Summary != issue.Closure.Reason {
			text += "\n" + evidence[0].Summary
		}
		body.WriteString("\n\n" + reportExcerpt(text, memberBudget))
	}
	id := x.IDs.New(protocol.PrefixMessage)
	// A system summary carries no actor authority and originates in Main even
	// when the closing write came from one of the individual Issue owners.
	if _, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: "main", MessageID: id, Author: "system", Body: body.String(), RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	pub := protocol.MessagingPublication{MessageID: id, Body: body.String(), Reason: "Requested combined Issue result", Purpose: "final", SourceMessageIDs: []string{source}, IssueIDs: ids}
	if err := registerCommunicationPublication(ctx, x, tx, id, pub, ""); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issue_completion_reports SET published_message_id=? WHERE source_message_id=? AND published_message_id IS NULL`, id, source)
	return err
}

func reportExcerpt(s string, limit int) string {
	units, last := 0, 0
	for pos, r := range s {
		if units < limit {
			last = pos
		}
		units += utf16.RuneLen(r)
		if units > limit {
			return s[:last] + "…"
		}
	}
	return s
}

// A grouped result belongs to the original request. An admitted user question,
// a separately requested update and a material change to the promise can still
// be communicated by any responsible member. No prose or stage label is parsed.
func checkCompletionReportPublication(ctx context.Context, q store.Querier, issues []string, turn store.MainTurn, p protocol.MessagingPublication) error {
	if p.NotificationBasis != "" {
		if (p.NotificationBasis != "requested_update" && p.NotificationBasis != "commitment_changed") || strings.TrimSpace(p.Reason) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "notification_basis requires requested_update or commitment_changed and its reason")
		}
		return nil
	}
	if p.ReplyTo != "" {
		var input, agreement bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_messages WHERE message_id=?1 AND direction='input'),
			EXISTS(SELECT 1 FROM issue_completion_reports WHERE source_message_id=?1)`, p.ReplyTo).Scan(&input, &agreement); err != nil {
			return err
		}
		if input {
			if !agreement {
				return nil
			}
			// The original agreement can be a fresh question for this execution
			// or historical material in another Issue's background batch.
			if admitted, err := mainExecutionSourceAdmitted(ctx, q, turn.RequestID, p.ReplyTo); err != nil || admitted {
				return err
			}
		}
	}
	if len(issues) == 0 {
		var err error
		issues, err = store.FollowupIDs(ctx, q, `SELECT work_item_id FROM message_work_items WHERE message_id=?1 AND active=1
			UNION SELECT work_item_id FROM background_batches WHERE message_id=?1 AND work_item_id<>''`, turn.MessageID)
		if err != nil || len(issues) == 0 {
			return err
		}
	}
	raw, err := json.Marshal(issues)
	if err != nil {
		return err
	}
	var grouped bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM json_each(?) scope
		JOIN message_work_items a ON a.work_item_id=scope.value AND a.active=1
		JOIN issue_completion_reports r ON r.source_message_id=a.message_id
		JOIN issue_controls c ON c.issue_id=scope.value
		WHERE (r.published_message_id IS NULL OR c.state='closed') AND scope.value IN (SELECT value FROM json_each(r.issue_ids_json)))`, string(raw)).Scan(&grouped); err != nil {
		return err
	}
	if grouped {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the original owner request has one combined completion report; record ordinary progress on the Issue, reply to a user question, or notify with the basis and reason for a separate requested update or material change")
	}
	return nil
}
