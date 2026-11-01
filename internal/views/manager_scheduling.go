package views

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func IssueManagerCandidates(ctx context.Context, q store.Querier) ([]scheduler.Candidate, error) {
	ids, err := store.FollowupIDs(ctx, q, `SELECT a.action_id FROM issue_manager_tasks m JOIN plan_actions a USING(task_id) JOIN issue_controls i ON i.issue_id=m.issue_id WHERE a.kind='coordinate' AND a.status='pending' AND ((m.state IN ('pending','assigned') AND i.state IN ('active','cancelling') AND i.assignment_state='active') OR json_extract(a.trigger_json,'$.receipt_only')=1) ORDER BY a.created_at,a.action_id`)
	if err != nil {
		return nil, err
	}
	var out []scheduler.Candidate
	for _, id := range ids {
		a, err := store.GetAction(ctx, q, id)
		if err != nil {
			return nil, err
		}
		t, err := store.GetTask(ctx, q, a.TaskID)
		if err != nil {
			return nil, err
		}
		project, err := store.GetProject(ctx, q, t.Project)
		if err != nil {
			return nil, err
		}
		m, err := store.GetIssueManagerTask(ctx, q, a.TaskID)
		if err != nil {
			return nil, err
		}
		i, err := store.GetCanonicalIssue(ctx, q, m.IssueID)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			ReceiptOnly bool `json:"receipt_only"`
		}
		if err = json.Unmarshal([]byte(a.TriggerJSON), &receipt); err != nil {
			return nil, err
		}
		if !receipt.ReceiptOnly && ((m.State == "pending" && i.AssignmentEpoch != m.ExpectedEpoch) || (m.State == "assigned" && i.AssignmentEpoch != m.AssignmentEpoch)) {
			continue
		}
		var occupied bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state IN ('starting','running','waiting','interrupted','unknown'))`, t.TaskID).Scan(&occupied); err != nil {
			return nil, err
		}
		if m.HandoffID != "" && !receipt.ReceiptOnly {
			blockers, err := ManagerHandoffBlockers(ctx, q, m.IssueID, m.ExpectedEpoch)
			if err != nil {
				return nil, err
			}
			occupied = occupied || len(blockers) > 0
		}
		if receipt.ReceiptOnly {
			t.Lifecycle = protocol.TaskQueued
			t.ControlMode = protocol.ControlManaged
			t.BlockReason = protocol.BlockNone
		}
		valid, err := store.IssueManagerAuthorizationValid(ctx, q, m.TaskID)
		if err != nil {
			return nil, err
		}
		policy, err := store.IssueUsesOwnerExecution(ctx, q, m.IssueID)
		if err != nil {
			return nil, err
		}
		ownershipPolicy := ""
		if policy {
			ownershipPolicy = protocol.IssueOwnerPolicy
		}
		rawSpec, err := store.GetSpec(ctx, q, m.TaskID, 1)
		if err != nil {
			return nil, err
		}
		var contract protocol.IssueManagerSpec
		if err := json.Unmarshal([]byte(rawSpec), &contract); err != nil {
			return nil, err
		}
		pending, err := ManagerReceiptPending(ctx, q, m)
		if err != nil {
			return nil, err
		}
		out = append(out, scheduler.Candidate{ManagerAuthorizationInvalid: !receipt.ReceiptOnly && !valid, Manager: &protocol.IssueManagerSpec{AgentID: contract.AgentID, OwnershipPolicy: ownershipPolicy, ReceiptOnly: receipt.ReceiptOnly, IssueID: m.IssueID, CategoryID: m.CategoryID, ExpectedAssignmentEpoch: m.ExpectedEpoch}, ManagerPin: m.Pin, ActionID: a.ActionID, TaskID: t.TaskID, Kind: a.Kind, Status: a.Status, Generation: a.Generation, TargetSpecVersion: 1, SpecVersion: 1, CreatedAt: a.CreatedAt, Lifecycle: t.Lifecycle, Control: t.ControlMode, Block: t.BlockReason, Rating: t.Rating, Weight: project.Weight, DependenciesDone: true, WorkspaceOccupied: occupied || (pending && !receipt.ReceiptOnly)})
	}
	return out, nil
}
