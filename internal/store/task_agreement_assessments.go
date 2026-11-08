package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// IssueAgreementDefinitionDigest identifies the responsibility's semantic
// definition, independently of title, progress and membership revisions.
func IssueAgreementDefinitionDigest(plan protocol.IssuePlan) (string, error) {
	criteria := append([]string{}, plan.CompletionCriteria...)
	background := append([]protocol.SharedFact{}, plan.Background...)
	raw, err := json.Marshal([]any{plan.Intent, plan.Scope, criteria, background})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ContextSourceDigest covers all sourced fact kinds and their actual versions,
// without task lifecycle counters or the act of recording a review itself.
func ContextSourceDigest(ctx context.Context, q Querier, issue string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT f.fact_id,f.kind,f.body,f.source_message_id,m.body,COALESCE(d.content_revision,1),COALESCE(r.revision,0),EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=m.message_id AND a.work_item_id=f.work_item_id AND a.active=1),EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=m.message_id AND c.withdrawn_at<>0) FROM work_item_context_facts f JOIN work_items w ON w.work_item_id=? JOIN conversation_messages m ON m.message_id=f.source_message_id LEFT JOIN message_dispositions d ON d.message_id=m.message_id LEFT JOIN message_association_revisions r ON r.message_id=m.message_id WHERE f.superseded=0 AND (f.work_item_id=w.work_item_id OR f.scope='global' OR (f.scope='project' AND f.project=w.project)) ORDER BY f.fact_id`, issue)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	facts := []any{}
	for rows.Next() {
		var id, kind, body, source, original string
		var content, association int64
		var linked, withdrawn bool
		if err := rows.Scan(&id, &kind, &body, &source, &original, &content, &association, &linked, &withdrawn); err != nil {
			return "", err
		}
		facts = append(facts, []any{id, kind, body, source, original, content, association, linked, withdrawn})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
