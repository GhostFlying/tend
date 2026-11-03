package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// TaskAcceptancePolicy reads only the immutable creation snapshot. Changes to an
// Issue's current policy never change already admitted work.
func TaskAcceptancePolicy(ctx context.Context, q Querier, task string) (*protocol.IssueAcceptancePolicy, error) {
	return readAcceptancePolicy(ctx, q, `SELECT policy_json FROM task_acceptance_policies WHERE task_id=?`, task)
}

func readAcceptancePolicy(ctx context.Context, q Querier, query, id string) (*protocol.IssueAcceptancePolicy, error) {
	var raw string
	if err := q.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if strings.Contains(err.Error(), "no such table") {
			var version int64
			if versionErr := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); versionErr != nil {
				return nil, versionErr
			}
			if version < 87 {
				return nil, nil
			}
		}
		return nil, err
	}
	var p protocol.IssueAcceptancePolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("store: decode acceptance policy: %w", err)
	}
	return &p, nil
}

func (t *Tx) FreezeTaskAcceptancePolicy(ctx context.Context, task string, p *protocol.IssueAcceptancePolicy) error {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO task_acceptance_policies(task_id,policy_json) VALUES(?,?)`, task, string(raw))
	return err
}
