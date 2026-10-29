package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/hostedconfig"
)

// ConsultationSelection is private local runtime state, never an API projection.
func ConsultationSelection(ctx context.Context, q Querier, id string) (hostedconfig.Selection, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT private_snapshot FROM consultation_conversations WHERE conversation_id=?`, id).Scan(&raw); err != nil {
		return hostedconfig.Selection{}, notFound(err, "consultation")
	}
	var selected hostedconfig.Selection
	err := json.Unmarshal([]byte(raw), &selected)
	return selected, err
}
