package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// initialIssueOwnerCandidates is a projection of current admitted definitions,
// not another publication ledger. Task/action identities do not yet exist.
func initialIssueOwnerCandidates(ctx context.Context, q store.Querier) ([]scheduler.Candidate, error) {
	ids, err := store.FollowupIDs(ctx, q, `SELECT i.issue_id FROM issue_controls i JOIN work_items w ON w.work_item_id=i.issue_id WHERE i.state='active' AND i.assignment_state='active' AND i.work_status IN ('todo','in_progress','in_review') AND i.owner_selection_json<>'' AND NOT EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.issue_id=i.issue_id AND m.state IN ('pending','assigned')) ORDER BY i.todo_at,i.issue_id`)
	if err != nil {
		return nil, err
	}
	var issues []protocol.Issue
	var selectedIDs []string
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if issue.OwnerSelection != nil {
			issues = append(issues, issue)
			selectedIDs = append(selectedIDs, id)
		}
	}
	if len(issues) == 0 {
		return nil, nil
	}
	barriers, err := initialOwnerHandoffBlockers(ctx, q, issues)
	if err != nil {
		return nil, err
	}
	admission, err := store.IssueAdmissionFacts(ctx, q, selectedIDs)
	if err != nil {
		return nil, err
	}
	var out []scheduler.Candidate
	for _, issue := range issues {
		id, choice := issue.IssueID, issue.OwnerSelection
		project, err := store.GetProject(ctx, q, issue.Plan.Project)
		if err != nil {
			return nil, err
		}
		c := scheduler.Candidate{InitialIssue: true, IssueID: id, ActionID: "issue:" + id, Kind: protocol.ActionCoordinate, Status: protocol.ActionPending, Generation: 1, SpecVersion: 1, TargetSpecVersion: 1, CreatedAt: issue.TodoAt, Lifecycle: protocol.TaskQueued, Control: protocol.ControlManaged, Block: protocol.BlockNone, Weight: project.Weight, DependenciesDone: true, Manager: &protocol.IssueManagerSpec{IssueID: id, CategoryID: choice.CategoryID, AgentID: choice.AgentID, OwnershipPolicy: choice.OwnershipPolicy, ExpectedAssignmentEpoch: issue.AssignmentEpoch}}
		category, err := store.GetAgentCategory(ctx, q, choice.CategoryID)
		if err != nil {
			return nil, err
		}
		if category.State != "active" {
			c.AdmissionMessage = "Selected owner capability category is inactive: " + choice.CategoryID
		}
		needed, err := store.IssueNeedsHostedMigration(ctx, q, issue)
		if err != nil {
			return nil, err
		}
		if needed {
			c.AdmissionMessage = "Existing hosted ownership requires the backed-up manager migration: " + id
		}
		blockers := barriers[id]
		if len(blockers) > 0 {
			c.AdmissionMessage = "Previous Issue owner execution or receipts require reconciliation: " + blockers[0].ObjectID
		}
		if err := applyIssueAdmissionFacts(&c, id, false, admission); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
