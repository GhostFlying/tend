package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// EffectiveContext freezes semantic inputs independently of plan/receipt counters.
// Progress messages and task-link revisions do not change an execution agreement.
func EffectiveContext(ctx context.Context, q Querier, id string) (string, []protocol.SharedFact, error) {
	w, err := GetWorkItem(ctx, q, id)
	if err != nil {
		return "", nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT f.fact_id,f.kind,f.body,f.source_message_id,m.body,COALESCE(d.content_revision,1),COALESCE((SELECT revision FROM message_association_revisions r WHERE r.message_id=m.message_id),0),f.needs_review,EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=m.message_id AND a.work_item_id=f.work_item_id AND a.active=1),EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=m.message_id AND c.withdrawn_at<>0) FROM work_item_context_facts f JOIN conversation_messages m ON m.message_id=f.source_message_id LEFT JOIN message_dispositions d ON d.message_id=m.message_id WHERE f.superseded=0 AND (f.work_item_id=? OR f.scope='global' OR (f.scope='project' AND f.project=?)) ORDER BY f.fact_id`, id, w.Project)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	records := []any{}
	facts := []protocol.SharedFact{}
	for rows.Next() {
		var fact, kind, body, source, original string
		var version, association int64
		var withdrawn, needsReview, associated bool
		if err := rows.Scan(&fact, &kind, &body, &source, &original, &version, &association, &needsReview, &associated, &withdrawn); err != nil {
			return "", nil, err
		}
		// Coordination promises, open questions and suggestions do not revise
		// an execution agreement. Their goal/constraint/authority consequences
		// must be registered in those explicit kinds before changing execution.
		oneShot := false
		if kind == "authorization" {
			oneShot, err = appliedTransitionReceipt(ctx, q, source, body)
			if err != nil {
				return "", nil, err
			}
		}
		if !oneShot && (kind == "goal" || kind == "constraint" || kind == "authorization" || kind == "refusal") {
			records = append(records, []any{fact, kind, body, source, original, version, association, needsReview, associated, withdrawn})
		}
		sharedKind := protocol.SharedFactFact
		switch kind {
		case "goal":
			sharedKind = protocol.SharedFactGoal
		case "constraint", "refusal":
			sharedKind = protocol.SharedFactUserConstraint
		case "authorization":
			sharedKind = protocol.SharedFactDecision
		}
		facts = append(facts, protocol.SharedFact{Kind: sharedKind, Text: body, Source: source})
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	// Editable plan changes already require an explicit current task disposition.
	// This fingerprint fences sourced facts without overriding continue_existing.
	raw, err := json.Marshal(records)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), facts, nil
}

// An applied panel choice authorizes only its recorded generation. Retain it as
// sourced audit evidence, but do not stale the unchanged spec it just retried.
// Provenance comes from the committed owner request, not model-supplied text.
func appliedTransitionReceipt(ctx context.Context, q Querier, source, body string) (bool, error) {
	var grant struct {
		Scope      string `json:"scope"`
		DecisionID string `json:"decision_id"`
		TaskID     string `json:"task_id"`
		RequestID  string `json:"request_id"`
	}
	if json.Unmarshal([]byte(body), &grant) != nil || grant.Scope != protocol.AppliedTransitionAuthorizationScope || grant.DecisionID == "" || grant.TaskID == "" || grant.RequestID == "" {
		return false, nil
	}
	var applied bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM request_records r JOIN conversation_messages m ON m.request_id=r.request_id JOIN decisions d ON d.decision_id=? WHERE r.domain='owner' AND r.request_id=? AND r.operation='decisions.respond' AND r.status='succeeded' AND m.message_id=? AND m.author='owner' AND d.task_id=? AND json_extract(r.result_json,'$.decision_id')=d.decision_id AND json_extract(r.result_json,'$.effect.applied')=1)`, grant.DecisionID, grant.RequestID, source, grant.TaskID).Scan(&applied)
	return applied, err
}
