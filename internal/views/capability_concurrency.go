package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RuntimeConcurrencyAvailable is shared by claim admission and capability
// explanations. Unknown/reserved/running executions all continue to count.
func RuntimeConcurrencyAvailable(ctx context.Context, q store.Querier, env, runtime, execution string, now int64) (bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT g.state_json FROM concurrency_bindings b JOIN concurrency_groups g USING(group_id) WHERE b.environment_id=? AND b.runtime_id=? AND g.deleted=0`, env, runtime).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var s struct {
		Group protocol.ConcurrencyGroup `json:"group"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return false, err
	}
	var held bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_executions WHERE execution_id=? AND state<>'stopped')`, execution).Scan(&held); err != nil {
		return false, err
	}
	if held {
		return true, nil
	}
	var occupied int64
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM concurrency_executions e JOIN concurrency_bindings b USING(environment_id,runtime_id) WHERE b.group_id=? AND e.state<>'stopped' AND NOT EXISTS(SELECT 1 FROM concurrency_executions child WHERE child.parent_execution_id=e.execution_id AND child.state<>'stopped')`, s.Group.ID).Scan(&occupied)
	return now >= s.Group.CooldownUntil && occupied < s.Group.Current, err
}
