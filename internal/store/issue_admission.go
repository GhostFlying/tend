package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// IssueAdmissionFacts loads only live control and prerequisite facts used by
// scheduling. Full aggregate/history consistency checks remain at their mutation
// boundaries; a claim need not decode every retained plan and snapshot twice.
func IssueAdmissionFacts(ctx context.Context, q Querier, ids []string) (map[string]protocol.Issue, error) {
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE needed(id) AS (
	 SELECT value FROM json_each(?)
	 UNION SELECT parent_issue_id FROM issue_controls JOIN needed ON issue_id=needed.id WHERE parent_issue_id IS NOT NULL
	 UNION SELECT depends_on FROM issue_dependencies JOIN needed ON issue_id=needed.id
	 ) SELECT i.issue_id,COALESCE(i.parent_issue_id,''),i.state,i.assignment_state,i.work_status,
	 COALESCE(json_extract(w.body_json,'$.closure.kind'),''),
	 COALESCE((SELECT json_group_array(depends_on) FROM issue_dependencies WHERE issue_id=i.issue_id),'[]')
	 FROM needed JOIN issue_controls i ON i.issue_id=needed.id JOIN work_items w ON w.work_item_id=i.issue_id`, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]protocol.Issue{}
	for rows.Next() {
		var issue protocol.Issue
		var closure, deps string
		if err := rows.Scan(&issue.IssueID, &issue.ParentIssueID, &issue.State, &issue.AssignmentState, &issue.WorkStatus, &closure, &deps); err != nil {
			return nil, err
		}
		if closure != "" {
			issue.Closure = &protocol.IssueClosure{Kind: protocol.IssueClosureKind(closure)}
		}
		if err := json.Unmarshal([]byte(deps), &issue.DependsOn); err != nil {
			return nil, fmt.Errorf("store: decode Issue prerequisites: %w", err)
		}
		out[issue.IssueID] = issue
	}
	return out, rows.Err()
}
