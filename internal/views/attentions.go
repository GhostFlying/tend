package views

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type AttentionFilter struct {
	IssueID, Kind, Status, After string
	IncludeArchived              bool
	Limit                        int
}

func Attention(ctx context.Context, q store.Querier, id string) (protocol.AttentionView, error) {
	a, err := store.GetAttention(ctx, q, id)
	if err != nil {
		return protocol.AttentionView{}, err
	}
	v := protocol.AttentionView{Attention: a}
	if a.DecisionID != "" {
		d, e := store.GetDecision(ctx, q, a.DecisionID)
		if e != nil {
			return v, e
		}
		value := Decision(d)
		v.Decision = &value
	}
	if a.ChoiceID != "" {
		d, e := store.GetWorkItemChoice(ctx, q, a.ChoiceID)
		if e != nil {
			return v, e
		}
		v.Choice = &d
	}
	return v, nil
}
func Attentions(ctx context.Context, q store.Querier, f AttentionFilter) (protocol.AttentionPage, error) {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 50
	}
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM attentions WHERE (?='' OR issue_id=?) AND (?='' OR kind=?) AND (?='' OR status=?) AND attention_id>? AND (? OR COALESCE(json_extract(body_json,'$.archived_at'),0)=0) ORDER BY attention_id LIMIT ?`, f.IssueID, f.IssueID, f.Kind, f.Kind, f.Status, f.Status, f.After, f.IncludeArchived, f.Limit+1)
	if err != nil {
		return protocol.AttentionPage{}, err
	}
	var ids []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return protocol.AttentionPage{}, err
		}
		var a protocol.Attention
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			rows.Close()
			return protocol.AttentionPage{}, err
		}
		ids = append(ids, a.AttentionID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return protocol.AttentionPage{}, err
	}
	page := protocol.AttentionPage{Items: []protocol.AttentionView{}}
	if len(ids) > f.Limit {
		page.More = true
		ids = ids[:f.Limit]
		page.Next = ids[len(ids)-1]
	}
	for _, id := range ids {
		v, e := Attention(ctx, q, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, v)
	}
	return page, nil
}
