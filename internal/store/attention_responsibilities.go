package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetAttentionResponsibility(ctx context.Context, q Querier, id string) (*protocol.AttentionResponsibility, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM attention_responsibilities WHERE attention_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r protocol.AttentionResponsibility
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (tx *Tx) SaveAttentionResponsibility(ctx context.Context, id string, r *protocol.AttentionResponsibility, expected int64, request string) error {
	ancestry, err := ResponsibilityAncestry(ctx, tx.Q(), r.OriginIssueID)
	if err != nil {
		return err
	}
	r.Ancestry = ancestry
	r.Revision = expected + 1
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO attention_responsibilities(attention_id,origin_issue_id,issue_id,recipient_kind,recipient_ref,assignment_epoch,revision,body_json) VALUES(?,?,NULLIF(?,''),?,?,?,?,?)`, id, r.OriginIssueID, r.IssueID, r.RecipientKind, r.RecipientRef, r.AssignmentEpoch, r.Revision, string(raw))
	} else {
		result, e := tx.Exec(ctx, `UPDATE attention_responsibilities SET issue_id=NULLIF(?,''),recipient_kind=?,recipient_ref=?,assignment_epoch=?,revision=?,body_json=? WHERE attention_id=? AND revision=?`, r.IssueID, r.RecipientKind, r.RecipientRef, r.AssignmentEpoch, r.Revision, string(raw), id, expected)
		err = e
		if err == nil {
			n, e := result.RowsAffected()
			err = e
			if err == nil && n != 1 {
				return protocol.Errorf(protocol.CodeStaleRevision, "problem responsibility changed")
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO attention_responsibility_history(attention_id,revision,body_json,request_id) VALUES(?,?,?,?)`, id, r.Revision, string(raw), request)
	return err
}

// ResponsibilityAncestry pins the hierarchy used to authorize escalation.
func ResponsibilityAncestry(ctx context.Context, q Querier, origin string) ([]protocol.AttentionResponsibilityAncestor, error) {
	var path []protocol.AttentionResponsibilityAncestor
	seen := map[string]bool{}
	for id := origin; id != ""; {
		if seen[id] {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "Issue hierarchy contains a cycle")
		}
		seen[id] = true
		i, err := GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return nil, err
		}
		path = append(path, protocol.AttentionResponsibilityAncestor{IssueID: id, AssignmentEpoch: i.AssignmentEpoch})
		id = i.ParentIssueID
	}
	return path, nil
}

// HumanProblem excludes internal responsibility from Inbox and channel cards.
func HumanProblem(ctx context.Context, q Querier, kind, id string) (bool, error) {
	var internal bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM attention_responsibilities r JOIN attentions a USING(attention_id) WHERE r.recipient_kind<>'owner' AND ((?='attention' AND a.attention_id=?) OR (?='decision' AND a.decision_id=?) OR (?='work_item_choice' AND a.choice_id=?)))`, kind, id, kind, id, kind, id).Scan(&internal)
	return !internal, err
}
