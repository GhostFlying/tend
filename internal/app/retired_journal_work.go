package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Return only retired journals with an actionable server-side fact. Background
// source reads also resume from their local durable cursor after retirement.
func retiredJournalWork(ctx context.Context, q store.Querier, host string, primary *protocol.RunnerRuntime) (map[string]protocol.RetiredJournalWork, error) {
	rows, err := q.QueryContext(ctx, `WITH retired AS (SELECT journal_id FROM runner_retirements WHERE host_id=? AND confirmed_at IS NOT NULL),
 outstanding AS (SELECT COALESCE(j.journal_id,'') journal_id FROM workspaces w JOIN workspace_runs wr USING(workspace_id) JOIN runs r ON r.run_id=wr.run_id AND r.task_id=w.occupied_by
 LEFT JOIN runner_journal_runs j ON j.run_id=r.run_id LEFT JOIN workspace_journal_releases p ON p.workspace_id=w.workspace_id AND p.task_id=r.task_id AND p.journal_id=COALESCE(j.journal_id,'')
 WHERE w.host_id=? AND NOT EXISTS(SELECT 1 FROM task_workspace_bindings b JOIN tasks t ON t.task_id=b.task_id WHERE b.task_id=w.occupied_by AND b.workspace_id=w.workspace_id AND t.lifecycle NOT IN ('completed','cancelled'))
 GROUP BY w.workspace_id,r.task_id,COALESCE(j.journal_id,'') HAVING COUNT(*)>COALESCE(MAX(p.run_count),0)),
 usage AS (SELECT DISTINCT COALESCE(j.journal_id,'') journal_id FROM token_usage_collection_commands c JOIN token_usage_sources s USING(source_key) LEFT JOIN runner_journal_runs j ON j.run_id=s.run_id WHERE c.host_id=? AND c.status='pending')
 SELECT journal_id,journal_id IN (SELECT journal_id FROM outstanding),journal_id IN (SELECT journal_id FROM usage) FROM retired WHERE journal_id IN (SELECT journal_id FROM outstanding UNION SELECT journal_id FROM usage)`, host, host, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]protocol.RetiredJournalWork{}
	var pending []string
	for rows.Next() {
		var id string
		var workspace, usage bool
		if err := rows.Scan(&id, &workspace, &usage); err != nil {
			return nil, err
		}
		out[id] = protocol.RetiredJournalWork{Workspace: workspace}
		if usage {
			pending = append(pending, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, id := range pending {
		source := *primary
		source.JournalID = id
		work := out[id]
		work.Usage, err = usageCollectionCommands(ctx, q, host, &source)
		if err != nil {
			return nil, err
		}
		out[id] = work
	}
	return out, nil
}
