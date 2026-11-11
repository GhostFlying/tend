package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/store"
)

// A runner manager selection reconstructs execution fields without copying the
// display name. Resolve its original Run's inventory, never today's assignee.
func issueConversationAuthor(ctx context.Context, q store.Querier, trigger string) (string, error) {
	var name, fallback, run string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(h.private_snapshot,'$.definition.display_name'),''),COALESCE(json_extract(h.private_snapshot,'$.identity.execution_environment_id')||'/'||json_extract(h.private_snapshot,'$.identity.local_id'),'负责人'),COALESCE(json_extract(h.private_snapshot,'$.runner_run_id'),'') FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE t.message_id=? ORDER BY h.created_at,h.execution_id LIMIT 1`, trigger).Scan(&name, &fallback, &run)
	if errors.Is(err, sql.ErrNoRows) {
		return "负责人", nil
	}
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}
	if run != "" {
		return runActivityAuthor(ctx, q, run, fallback)
	}
	return fallback, nil
}

func runActivityAuthor(ctx context.Context, q store.Querier, run, fallback string) (string, error) {
	return activityDisplayName(ctx, q, fallback, `WITH pins(pin_json) AS (SELECT pin_json FROM run_agent_executions WHERE run_id=? UNION ALL SELECT m.pin_json FROM issue_manager_tasks m JOIN runs r USING(task_id) WHERE r.run_id=? AND r.role='issue_manager' AND m.pin_json<>'' AND NOT EXISTS(SELECT 1 FROM run_agent_executions e WHERE e.run_id=r.run_id)) SELECT COALESCE(json_extract(a.value,'$.display_name'),'') FROM pins e JOIN agent_inventory_versions v ON v.host_id=json_extract(e.pin_json,'$.identity.execution_environment_id') AND v.local_revision=json_extract(e.pin_json,'$.inventory_revision'),json_each(v.projection_json,'$.agents') a WHERE json_extract(a.value,'$.identity.local_id')=json_extract(e.pin_json,'$.identity.local_id') LIMIT 1`, run, run)
}
