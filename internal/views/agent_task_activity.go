package views

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The host/state index bounds work to this runner's unsettled Runs before
// decoding their frozen identity. Keep the identity predicate as provenance.
const agentTaskActivitySQL = `WITH tasks AS (
 SELECT json_extract(e.pin_json,'$.identity.local_id') AS agent,r.task_id,
 MAX(r.observed_state IN ('starting','running','waiting')) AS running
 FROM runs r JOIN run_agent_executions e USING(run_id)
 WHERE r.host_id=? AND json_extract(e.pin_json,'$.identity.execution_environment_id')=?
 AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')
 GROUP BY agent,r.task_id
) SELECT agent,SUM(running),SUM(1-running) FROM tasks GROUP BY agent`

// AgentTaskActivity counts Task identities, not Run attempts or mutable owners.
// An interrupted/unknown execution needs reconciliation; it is not proof of work
// continuing or of an idle executor. Host presence does not rewrite Run facts.
func AgentTaskActivity(ctx context.Context, q store.Querier, environment string) (*protocol.AgentTaskActivity, error) {
	out := &protocol.AgentTaskActivity{RunningTasks: map[string]int{}, UncertainTasks: map[string]int{}}
	rows, err := q.QueryContext(ctx, agentTaskActivitySQL, environment, environment)
	if err != nil {
		return nil, fmt.Errorf("views: Agent task activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		var running, uncertain int
		if err := rows.Scan(&agent, &running, &uncertain); err != nil {
			return nil, err
		}
		out.RunningTasks[agent], out.UncertainTasks[agent] = running, uncertain
	}
	return out, rows.Err()
}
