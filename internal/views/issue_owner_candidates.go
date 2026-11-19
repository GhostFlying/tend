package views

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// initialIssueOwnerCandidates is a projection of current admitted definitions,
// not another publication ledger. Task/action identities do not yet exist.
func initialIssueOwnerCandidates(ctx context.Context, q store.Querier) ([]scheduler.Candidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.issue_id,i.assignment_epoch,i.todo_at,i.owner_selection_json,
 i.assignee_kind,i.assignee_ref,p.weight,c.state
 FROM issue_controls i JOIN work_items w ON w.work_item_id=i.issue_id
 LEFT JOIN projects p ON p.project=w.project
 LEFT JOIN agent_category_versions c ON c.category_id=json_extract(i.owner_selection_json,'$.category_id') AND c.revision=(SELECT MAX(v.revision) FROM agent_category_versions v WHERE v.category_id=c.category_id)
 WHERE i.state='active' AND i.assignment_state='active' AND i.work_status IN ('todo','in_progress','in_review') AND i.owner_selection_json<>''
 AND NOT EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.issue_id=i.issue_id AND m.state IN ('pending','assigned')) ORDER BY i.todo_at,i.issue_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scheduler.Candidate
	ids := []string{}
	epochs := map[string]int64{}
	legacy := map[string]protocol.Issue{}
	for rows.Next() {
		var id, raw, kind, ref, category string
		var epoch, todo, weight int64
		if err := rows.Scan(&id, &epoch, &todo, &raw, &kind, &ref, &weight, &category); err != nil {
			return nil, err
		}
		var choice *protocol.IssueOwnerSelection
		if err := json.Unmarshal([]byte(raw), &choice); err != nil {
			return nil, err
		}
		if choice == nil {
			continue
		}
		control := protocol.IssueControl{IssueID: id, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeKind(kind), Ref: ref}, AssignmentEpoch: epoch}
		agentID, err := store.IssueOwnerAgentID(ctx, q, control, choice.AgentID)
		if err != nil {
			return nil, err
		}
		c := scheduler.Candidate{InitialIssue: true, IssueID: id, ActionID: "issue:" + id, Kind: protocol.ActionCoordinate, Status: protocol.ActionPending, Generation: 1, SpecVersion: 1, TargetSpecVersion: 1, CreatedAt: todo, Lifecycle: protocol.TaskQueued, Control: protocol.ControlManaged, Block: protocol.BlockNone, Weight: weight, DependenciesDone: true, Manager: &protocol.IssueManagerSpec{IssueID: id, CategoryID: choice.CategoryID, AgentID: agentID, OwnershipPolicy: choice.OwnershipPolicy, ExpectedAssignmentEpoch: epoch}}
		if category != "active" {
			c.AdmissionMessage = "Selected owner capability category is inactive: " + choice.CategoryID
		}
		// Historical hosted adoption retains its full original/migration checks.
		// Ordinary unclaimed definitions need no historical aggregate decoding.
		if kind == string(protocol.IssueAssigneeAgent) {
			legacy[id] = protocol.Issue{IssueControl: control}
		}

		out = append(out, c)
		ids = append(ids, id)
		epochs[id] = epoch + 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	blockers, err := managerHandoffBlockersBatch(ctx, q, epochs)
	if err != nil {
		return nil, err
	}
	admission, err := store.IssueAdmissionFacts(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		if issue, ok := legacy[c.IssueID]; ok {
			needed, err := store.IssueNeedsHostedMigration(ctx, q, issue)
			if err != nil {
				return nil, err
			}
			if needed {
				c.AdmissionMessage = "Existing hosted ownership requires the backed-up manager migration: " + c.IssueID
			}
		}
		if prior := blockers[c.IssueID]; len(prior) > 0 {
			c.AdmissionMessage = "Previous Issue owner execution or receipts require reconciliation: " + prior[0].ObjectID
		}
		if err := applyIssueAdmissionFacts(c, c.IssueID, false, admission); err != nil {
			return nil, err
		}
	}
	return out, nil
}
