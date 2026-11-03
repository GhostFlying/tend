package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// QuestionInputs folds only this question's indexed grant/revoke ledger.
func QuestionInputs(ctx context.Context, q Querier, decision string) (protocol.QuestionInputPage, error) {
	out := protocol.QuestionInputPage{DecisionID: decision, Grants: []protocol.QuestionInputGrant{}, NextStep: "Read the original run question for file metadata, then fetch exact file_id with that run credential. This API grants only the original run. A successor needs its own authorized question or an independently approved spec input."}
	rows, err := q.QueryContext(ctx, `SELECT object_version,kind,body_json FROM events WHERE object_type='question_inputs' AND object_id=? ORDER BY event_seq`, decision)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var revision int64
		var kind, raw string
		if err := rows.Scan(&revision, &kind, &raw); err != nil {
			return out, err
		}
		if revision != out.Revision+1 {
			return out, fmt.Errorf("store: invalid question input revision")
		}
		out.Revision = revision
		switch kind {
		case "question.inputs_granted":
			var body struct {
				Grant protocol.QuestionInputGrant `json:"grant"`
			}
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				return out, err
			}
			g := body.Grant
			if g.Format != 1 || g.DecisionID != decision || g.Revision != revision || g.Status != "active" {
				return out, fmt.Errorf("store: invalid question input grant")
			}
			out.Grants = append(out.Grants, g)
		case "question.inputs_revoked":
			var r struct {
				GrantRevision int64 `json:"grant_revision"`
			}
			if err := json.Unmarshal([]byte(raw), &r); err != nil {
				return out, err
			}
			found := false
			for i := range out.Grants {
				if out.Grants[i].Revision == r.GrantRevision {
					out.Grants[i].Status = "revoked"
					found = true
				}
			}
			if !found {
				return out, fmt.Errorf("store: missing question input grant")
			}
		default:
			return out, fmt.Errorf("store: invalid question input event")
		}
	}
	return out, rows.Err()
}
