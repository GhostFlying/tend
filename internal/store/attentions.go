package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetAttention(ctx context.Context, q Querier, id string) (protocol.Attention, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM attentions WHERE attention_id=?`, id).Scan(&raw); err != nil {
		return protocol.Attention{}, notFound(err, "attention")
	}
	var a protocol.Attention
	err := json.Unmarshal([]byte(raw), &a)
	if err == nil {
		a.Responsibility, err = GetAttentionResponsibility(ctx, q, id)
	}
	return a, err
}
func (tx *Tx) SaveAttention(ctx context.Context, a protocol.Attention, expected int64, request string) error {
	a.Responsibility = nil // The independently versioned route has one authority.
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO attentions(attention_id,issue_id,decision_id,choice_id,kind,status,revision,body_json,updated_at) VALUES(?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,?,?)`, a.AttentionID, a.IssueID, a.DecisionID, a.ChoiceID, a.Kind, a.Status, a.Revision, string(raw), a.UpdatedAt)
	} else {
		result, e := tx.Exec(ctx, `UPDATE attentions SET status=?,revision=?,body_json=?,updated_at=? WHERE attention_id=? AND revision=?`, a.Status, a.Revision, string(raw), a.UpdatedAt, a.AttentionID, expected)
		err = e
		if err == nil {
			n, e := result.RowsAffected()
			err = e
			if err == nil && n != 1 {
				return protocol.Errorf(protocol.CodeStaleRevision, "attention changed")
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO attention_revisions(attention_id,revision,body_json,request_id) VALUES(?,?,?,?)`, a.AttentionID, a.Revision, string(raw), request)
	return err
}

// CanDismissAttention checks the persisted classification and the current
// Inbox recipient. Callers doing a write must pass the transaction's snapshot.
func CanDismissAttention(ctx context.Context, q Querier, a protocol.Attention, ownerSubject string) (bool, error) {
	if ownerSubject == "" || a.ActionClass != protocol.AttentionActionOrdinary ||
		a.Kind != "action" || a.Status != "open" || a.BlocksClose ||
		a.TaskID != "" || a.DecisionID != "" || a.ChoiceID != "" || a.ReportFollowOn != nil {
		return false, nil
	}
	r := a.Responsibility
	if r == nil {
		return a.IssueID == "" && a.RaisedBy.Kind == protocol.PrincipalOwner && a.RaisedBy.Subject == ownerSubject, nil
	}
	if r.RecipientKind != "owner" || (r.RecipientRef != "" && r.RecipientRef != ownerSubject) ||
		r.OriginIssueID != a.IssueID || r.Revision < 1 {
		return false, nil
	}
	ancestry, err := ResponsibilityAncestry(ctx, q, a.IssueID)
	if err != nil {
		return false, err
	}
	if !slices.Equal(ancestry, r.Ancestry) {
		return false, nil
	}
	origin, err := GetCanonicalIssue(ctx, q, a.IssueID)
	if err != nil || origin.AssignmentEpoch != r.OriginAssignmentEpoch {
		return false, err
	}
	if r.IssueID == "" {
		return true, nil
	}
	recipient, err := GetCanonicalIssue(ctx, q, r.IssueID)
	if err != nil {
		return false, err
	}
	return recipient.AssignmentEpoch == r.AssignmentEpoch && recipient.Assignee.Kind == protocol.IssueAssigneeOwner &&
		recipient.Assignee.Ref == r.RecipientRef, nil
}
