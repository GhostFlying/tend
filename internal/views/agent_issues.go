package views

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AgentIssueFilter restricts frozen execution evidence, not the current definition.
type AgentIssueFilter struct {
	Identity      protocol.AgentIdentity
	Scope         string
	State         string
	Search        string
	After         string
	Limit         int
	SnapshotID    string
	AgentRevision int64
}

func (f AgentIssueFilter) Validate() error {
	if !protocol.AgentInventoryID(f.Identity.ExecutionEnvironmentID) || !protocol.AgentInventoryID(f.Identity.LocalID) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "agent requires environment_id and local_id")
	}
	if f.Scope != "" && f.Scope != "all" && f.Scope != "current" && f.Scope != "history" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "scope must be all, current or history")
	}
	if f.State != "" && f.State != "active" && f.State != "paused" && f.State != "cancelling" && f.State != "cancelled" && f.State != "closed" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid Issue state")
	}
	if f.Limit < 0 || f.Limit > 100 || f.AgentRevision < 0 || (f.SnapshotID != "" && !protocol.AgentInventoryID(f.SnapshotID)) || (f.After != "" && !protocol.ValidID(protocol.PrefixWorkItem, f.After)) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid pagination or definition filter")
	}
	if len(f.Search) > 500 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "search is too long")
	}
	return nil
}

// Frozen provenance is ranked: exact Run responsibility, manager contract,
// then the task's creation-time canonical owner. Mutable task links are excluded.
const agentIssuesCTE = `WITH decision_tasks AS (
 SELECT DISTINCT d.task_id,t.spec_version FROM decisions d JOIN tasks t USING(task_id)
 WHERE d.status='open' AND t.lifecycle NOT IN ('completed','cancelled','failed')
), pinned AS (
 SELECT r.run_id,r.task_id,r.role,r.generation,r.host_id,r.observed_state,a.target_spec_version,e.pin_json,
 (d.task_id IS NOT NULL AND d.spec_version=a.target_spec_version) AS pending_decision
 FROM run_agent_executions e JOIN runs r USING(run_id) JOIN plan_actions a USING(action_id) LEFT JOIN decision_tasks d ON d.task_id=r.task_id
 WHERE json_extract(e.pin_json,'$.identity.execution_environment_id')=?
 AND json_extract(e.pin_json,'$.identity.local_id')=?
 AND (?='' OR json_extract(e.pin_json,'$.snapshot_id')=?)
 AND (?=0 OR json_extract(e.pin_json,'$.agent_revision')=?)
), linked AS (
 SELECT p.*,d.issue_id,'run_responsibility' AS source FROM pinned p JOIN run_issue_responsibilities d USING(run_id)
 UNION ALL
 SELECT p.*,m.issue_id,'manager_contract' FROM pinned p JOIN issue_manager_tasks m USING(task_id)
 WHERE NOT EXISTS(SELECT 1 FROM run_issue_responsibilities d WHERE d.run_id=p.run_id)
 UNION ALL
 SELECT p.*,o.work_item_id,'task_creation_owner' FROM pinned p JOIN task_work_item_owners o USING(task_id)
 WHERE NOT EXISTS(SELECT 1 FROM run_issue_responsibilities d WHERE d.run_id=p.run_id)
 AND NOT EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.task_id=p.task_id)
), grouped AS (
 SELECT l.issue_id,MAX(l.observed_state IN ('starting','running','waiting','interrupted','unknown') OR l.pending_decision) AS is_current
 FROM linked l JOIN issue_controls c USING(issue_id) GROUP BY l.issue_id
), selected AS (
 SELECT g.issue_id,g.is_current,c.state,COALESCE(json_extract(w.body_json,'$.title'),'') AS title
 FROM grouped g JOIN issue_controls c USING(issue_id) JOIN work_items w ON w.work_item_id=g.issue_id
 WHERE (?='' OR ?='all' OR (?='current' AND g.is_current=1) OR (?='history' AND g.is_current=0))
 AND (?='' OR c.state=?) AND (?='' OR instr(lower(COALESCE(json_extract(w.body_json,'$.title'),'')),lower(?))>0 OR instr(g.issue_id,?)>0)
 AND (?='' OR g.issue_id>?) ORDER BY g.issue_id LIMIT ?
)
SELECT s.issue_id,s.title,s.state,s.is_current,l.task_id,l.run_id,l.role,l.generation,l.target_spec_version,COALESCE(l.host_id,''),l.observed_state,l.pin_json,l.source,l.pending_decision
FROM selected s JOIN linked l USING(issue_id) ORDER BY s.issue_id,l.run_id`

// AgentIssues is one bounded Issue page in its caller's read transaction. All
// runs of each returned Issue are retained rather than silently truncating history.
func AgentIssues(ctx context.Context, q store.Querier, f AgentIssueFilter) (protocol.AgentIssuePage, error) {
	out := protocol.AgentIssuePage{Identity: f.Identity, Coverage: "recorded_run_pins", Items: []protocol.AgentIssue{}}
	if err := f.Validate(); err != nil {
		return out, err
	}
	if f.Limit == 0 {
		f.Limit = 25
	}
	rows, err := q.QueryContext(ctx, agentIssuesCTE, f.Identity.ExecutionEnvironmentID, f.Identity.LocalID, f.SnapshotID, f.SnapshotID, f.AgentRevision, f.AgentRevision, f.Scope, f.Scope, f.Scope, f.Scope, f.State, f.State, f.Search, f.Search, f.Search, f.After, f.After, f.Limit+1)
	if err != nil {
		return out, fmt.Errorf("views: Agent Issue associations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, state, pin string
		var current bool
		var run protocol.AgentIssueRun
		if err := rows.Scan(&id, &title, &state, &current, &run.TaskID, &run.Run.RunID, &run.Run.Role, &run.Run.Generation, &run.Run.SpecVersion, &run.Run.Host, &run.Run.Observed, &pin, &run.Source, &run.PendingDecision); err != nil {
			return out, err
		}
		if len(out.Items) == 0 || out.Items[len(out.Items)-1].IssueID != id {
			if len(out.Items) == f.Limit {
				out.More = true
				break
			}
			out.Items = append(out.Items, protocol.AgentIssue{IssueID: id, Title: title, State: protocol.IssueState(state), Current: current, Runs: []protocol.AgentIssueRun{}})
		}
		if err := json.Unmarshal([]byte(pin), &run.Pin); err != nil {
			return out, fmt.Errorf("views: Agent execution pin: %w", err)
		}
		out.Items[len(out.Items)-1].Runs = append(out.Items[len(out.Items)-1].Runs, run)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.More {
		out.Next = out.Items[len(out.Items)-1].IssueID
	}
	return out, nil
}
