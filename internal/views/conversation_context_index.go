package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ConversationContextIndex reads metadata directly: large original bodies are never loaded.
func ConversationContextIndex(ctx context.Context, q store.Querier, id string, query protocol.ConversationContextQuery) (protocol.ConversationContextIndex, error) {
	query, err := protocol.NormalizeConversationContextQuery(query)
	v := protocol.ConversationContextIndex{Format: "index-v1", ConversationID: store.ConversationID(id), Section: query.Section, Items: []protocol.ConversationContextReference{}, Coverage: "Current references only. Reading confirms no review. Native records may be incomplete; retained messages and originals are separate evidence."}
	if err != nil {
		return v, err
	}
	err = q.QueryRowContext(ctx, `SELECT c.revision,COALESCE(c.work_item_id,''),c.last_message_seq,CASE WHEN c.conversation_id='main' THEN 'main' WHEN EXISTS(SELECT 1 FROM consultation_conversations s WHERE s.conversation_id=c.conversation_id) THEN 'consultation' ELSE 'issue' END FROM agent_conversations c WHERE c.conversation_id=?`, v.ConversationID).Scan(&v.Revision, &v.IssueID, &v.ThroughMessageSeq, &v.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return v, protocol.Errorf(protocol.CodeNotFound, "conversation not found")
	}
	if err != nil {
		return v, err
	}
	v.ThroughEventSeq, err = store.LatestEventSeq(ctx, q)
	if err != nil {
		return v, err
	}
	if v.IssueID != "" {
		err = q.QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, v.IssueID).Scan(&v.ContextRevision)
		if err != nil {
			return v, err
		}
		err = q.QueryRowContext(ctx, `SELECT state,assignment_epoch FROM issue_controls WHERE issue_id=?`, v.IssueID).Scan(&v.State, &v.AssignmentEpoch)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return v, err
		}
	}
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.needs_review=1 AND (?='' OR o.work_item_id=?))`, v.IssueID, v.IssueID).Scan(&v.ReviewRequired)
	if err != nil {
		return v, err
	}
	if v.Kind == "consultation" {
		v.ReviewRequired = false
	}
	// Explicitly inherited contexts grant references, never ancestor control.
	scope := `WITH RECURSIVE scope(id) AS (SELECT ? UNION SELECT c.parent_issue_id FROM issue_controls c JOIN scope s ON c.issue_id=s.id WHERE c.parent_issue_id IS NOT NULL) `
	err = q.QueryRowContext(ctx, scope+`SELECT
    (SELECT COUNT(*) FROM conversation_messages m WHERE m.kind='message' AND (m.conversation_id=? OR (?<>'' AND EXISTS(SELECT 1 FROM message_work_items w WHERE w.message_id=m.message_id AND w.active=1 AND w.work_item_id IN (SELECT id FROM scope))))),
    (SELECT COUNT(*) FROM work_item_context_facts f WHERE ?='main' OR (?<>'' AND (f.work_item_id IN (SELECT id FROM scope) OR f.scope='global' OR (f.scope='project' AND f.project=(SELECT project FROM work_items WHERE work_item_id=?))))),
    (SELECT COUNT(*) FROM agent_thread_history WHERE conversation_id=?)`, v.IssueID, v.ConversationID, v.IssueID, v.Kind, v.IssueID, v.IssueID, v.ConversationID).Scan(&v.SourceCount, &v.FactCount, &v.ThreadCount)
	if err != nil {
		return v, err
	}
	if query.Section == "state" {
		return v, nil
	}
	var rows *sql.Rows
	switch query.Section {
	case "sources":
		rows, err = q.QueryContext(ctx, scope+`SELECT m.message_id,m.conversation_id,m.seq,COALESCE(d.content_revision,1),COALESCE(a.revision,0),COALESCE(c.withdrawn_at,0)<>0,EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.message_id=m.message_id AND o.needs_review=1 AND (?='' OR o.work_item_id=?)) FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN message_association_revisions a USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.kind='message' AND m.message_id>? AND (m.conversation_id=? OR (?<>'' AND EXISTS(SELECT 1 FROM message_work_items w WHERE w.message_id=m.message_id AND w.active=1 AND w.work_item_id IN (SELECT id FROM scope)))) ORDER BY m.message_id LIMIT ?`, v.IssueID, v.IssueID, v.IssueID, query.After, v.ConversationID, v.IssueID, query.Limit+1)
	case "facts":
		rows, err = q.QueryContext(ctx, scope+`SELECT f.fact_id,COALESCE(f.work_item_id,''),f.source_message_id,f.needs_review,f.superseded,m.seq,m.conversation_id FROM work_item_context_facts f JOIN conversation_messages m ON m.message_id=f.source_message_id WHERE f.fact_id>? AND (?='main' OR (?<>'' AND (f.work_item_id IN (SELECT id FROM scope) OR f.scope='global' OR (f.scope='project' AND f.project=(SELECT project FROM work_items WHERE work_item_id=?))))) ORDER BY f.fact_id LIMIT ?`, v.IssueID, query.After, v.Kind, v.IssueID, v.IssueID, query.Limit+1)
	case "threads":
		rows, err = q.QueryContext(ctx, `SELECT h.thread_id,COALESCE((SELECT b.path FROM main_thread_bindings b WHERE b.thread_id=h.thread_id ORDER BY b.created_at DESC LIMIT 1),'') FROM agent_thread_history h WHERE h.conversation_id=? AND h.thread_id>? ORDER BY h.thread_id LIMIT ?`, v.ConversationID, query.After, query.Limit+1)
	}
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		r := protocol.ConversationContextReference{Kind: query.Section}
		switch query.Section {
		case "sources":
			err = rows.Scan(&r.ID, &r.ConversationID, &r.Seq, &r.ContentRevision, &r.AssociationRevision, &r.Withdrawn, &r.NeedsReview)
		case "facts":
			err = rows.Scan(&r.ID, &r.IssueID, &r.SourceMessageID, &r.NeedsReview, &r.Superseded, &r.Seq, &r.ConversationID)
		case "threads":
			err = rows.Scan(&r.ID, &r.RolloutPath)
		}
		if err != nil {
			return v, err
		}
		if len(v.Items) == query.Limit {
			v.More = true
			break
		}
		v.Items = append(v.Items, r)
	}
	if err = rows.Err(); err != nil {
		return v, err
	}
	if err = rows.Close(); err != nil {
		return v, err
	}
	// Keep pagination position even when an unadmitted original is hidden below.
	if len(v.Items) > 0 {
		v.Next = v.Items[len(v.Items)-1].ID
	}
	if query.Section == "sources" || query.Section == "facts" {
		visible := v.Items[:0]
		for _, r := range v.Items {
			source := r.ID
			if query.Section == "facts" {
				source = r.SourceMessageID
			}
			admitted, err := store.OwnerInputAdmitted(ctx, q, source)
			if err != nil {
				return v, err
			}
			if admitted {
				visible = append(visible, r)
			}
		}
		v.Items = visible
	}
	return v, nil
}
