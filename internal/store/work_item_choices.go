package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
)

func GetWorkItemChoice(ctx context.Context, q Querier, id string) (protocol.WorkItemChoice, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM work_item_choices WHERE choice_id=?`, id).Scan(&raw); err != nil {
		return protocol.WorkItemChoice{}, notFound(err, "choice")
	}
	var c protocol.WorkItemChoice
	err := json.Unmarshal([]byte(raw), &c)
	return c, err
}
func ListWorkItemChoices(ctx context.Context, q Querier, itemID, messageID string) ([]protocol.WorkItemChoice, error) {
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM work_item_choices WHERE (?='' OR work_item_id=?) AND (?='' OR message_id=?) ORDER BY created_at,choice_id`, itemID, itemID, messageID, messageID)
	if err != nil {
		return nil, fmt.Errorf("store: list choices: %w", err)
	}
	defer rows.Close()
	out := []protocol.WorkItemChoice{}
	for rows.Next() {
		var raw string
		var c protocol.WorkItemChoice
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (t *Tx) SaveWorkItemChoice(ctx context.Context, c protocol.WorkItemChoice, sourceKey string, expected int64) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = t.Exec(ctx, `INSERT INTO work_item_choices(choice_id,work_item_id,source_key,version,status,body_json,message_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, c.ChoiceID, c.WorkItemID, sourceKey, c.Version, c.Status, string(raw), c.MessageID, c.CreatedAt, c.UpdatedAt)
		return err
	}
	res, err := t.Exec(ctx, `UPDATE work_item_choices SET version=?,status=?,body_json=?,updated_at=? WHERE choice_id=? AND version=?`, c.Version, c.Status, string(raw), c.UpdatedAt, c.ChoiceID, expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return protocol.Errorf(protocol.CodeStaleRevision, "choice changed")
	}
	return err
}
