package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func issueSteerSender(sender protocol.Principal) string {
	if sender.Kind == protocol.PrincipalMainAgent {
		return namedExecutionAuthor("主 Agent", sender.Subject)
	}
	return namedExecutionAuthor("操作员", sender.Subject)
}

// A runner manager selection reconstructs execution fields without copying the
// display name. Resolve its original Run's inventory, never today's assignee.
func issueConversationAuthor(ctx context.Context, q store.Querier, trigger string) (string, error) {
	var name, fallback, run string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(h.private_snapshot,'$.definition.display_name'),''),COALESCE(json_extract(h.private_snapshot,'$.identity.execution_environment_id')||'/'||json_extract(h.private_snapshot,'$.identity.local_id'),'Agent 身份未记录'),COALESCE(json_extract(h.private_snapshot,'$.runner_run_id'),'') FROM hosted_executions h WHERE h.execution_id=COALESCE(NULLIF((SELECT execution_id FROM messaging_messages WHERE message_id=?),''),(SELECT request_id FROM main_turns WHERE message_id=? ORDER BY created_at,request_id LIMIT 1))`, trigger, trigger).Scan(&name, &fallback, &run)
	if errors.Is(err, sql.ErrNoRows) {
		return "Agent 身份未记录", nil
	}
	if err != nil {
		return "", err
	}
	if name != "" {
		return namedExecutionAuthor(name, fallback), nil
	}
	if run != "" {
		return runActivityAuthor(ctx, q, run, fallback)
	}
	return fallback, nil
}

func runActivityAuthor(ctx context.Context, q store.Querier, run, fallback string) (string, error) {
	return activityDisplayName(ctx, q, fallback, `WITH pins(pin_json) AS (SELECT pin_json FROM run_agent_executions WHERE run_id=? UNION ALL SELECT m.pin_json FROM issue_manager_tasks m JOIN runs r USING(task_id) WHERE r.run_id=? AND r.role='issue_manager' AND m.pin_json<>'' AND NOT EXISTS(SELECT 1 FROM run_agent_executions e WHERE e.run_id=r.run_id)) SELECT COALESCE(CASE WHEN COALESCE(json_extract(a.value,'$.display_name'),'')<>'' THEN json_extract(a.value,'$.display_name')||' · '||json_extract(e.pin_json,'$.identity.execution_environment_id')||'/'||json_extract(e.pin_json,'$.identity.local_id') ELSE json_extract(e.pin_json,'$.identity.execution_environment_id')||'/'||json_extract(e.pin_json,'$.identity.local_id') END,'') FROM pins e LEFT JOIN agent_inventory_versions v ON v.host_id=json_extract(e.pin_json,'$.identity.execution_environment_id') AND v.local_revision=json_extract(e.pin_json,'$.inventory_revision') LEFT JOIN json_each(v.projection_json,'$.agents') a ON json_extract(a.value,'$.identity.local_id')=json_extract(e.pin_json,'$.identity.local_id') LIMIT 1`, run, run)
}

// Keep the frozen name and concrete execution identity together when available.
func namedExecutionAuthor(name, identity string) string {
	if identity == "" || identity == "Agent 身份未记录" {
		return name
	}
	if name == identity {
		return name
	}
	return name + " · " + identity
}
