package store

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
)

func GetAttention(ctx context.Context, q Querier, id string) (protocol.Attention, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM attentions WHERE attention_id=?`, id).Scan(&raw); err != nil {
		return protocol.Attention{}, notFound(err, "attention")
	}
	var a protocol.Attention
	err := json.Unmarshal([]byte(raw), &a)
	return a, err
}
func (tx *Tx) SaveAttention(ctx context.Context, a protocol.Attention, expected int64, request string) error {
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
