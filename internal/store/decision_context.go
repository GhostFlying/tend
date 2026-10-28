package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// GetDecisionBriefing returns the latest retained explanation, if one exists.
func GetDecisionBriefing(ctx context.Context, q Querier, id string) (*protocol.DecisionBriefing, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM decision_briefings WHERE decision_id=? ORDER BY revision DESC LIMIT 1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read decision briefing: %w", err)
	}
	var b protocol.DecisionBriefing
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, fmt.Errorf("store: decode decision briefing: %w", err)
	}
	return &b, nil
}
