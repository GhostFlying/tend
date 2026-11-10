package views

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func IssueManagerCandidates(ctx context.Context, q store.Querier) ([]scheduler.Candidate, error) {
	return IssueManagerCandidatesObserved(ctx, q, nil)
}

// IssueManagerCandidatesObserved exposes fixed inclusive diagnostic boundaries
// without logging queries or retained Issue/specification bodies.
func IssueManagerCandidatesObserved(ctx context.Context, q store.Querier, observe func(store.WritePhase) func()) ([]scheduler.Candidate, error) {
	end := func() {}
	if observe != nil {
		end = observe(store.WriteClaimManagerBuild)
	}
	defer end()

	rows, err := managerSchedulingRows(ctx, q, observe)
	if err != nil {
		return nil, err
	}
	out, err := initialIssueOwnerCandidates(ctx, q)
	if err != nil {
		return nil, err
	}
	issueIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		issueIDs = append(issueIDs, row.manager.IssueID)
	}
	admission, err := store.IssueAdmissionFacts(ctx, q, issueIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		a, t, m := row.action, row.task, row.manager
		var contract protocol.IssueManagerSpec
		if err := json.Unmarshal([]byte(row.spec), &contract); err != nil {
			return nil, err
		}
		if row.pin != "" {
			m.Pin = new(protocol.RunExecutionPin)
			if err := json.Unmarshal([]byte(row.pin), m.Pin); err != nil {
				return nil, err
			}
		}
		var receipt struct {
			ReceiptOnly bool `json:"receipt_only"`
		}
		if err = json.Unmarshal([]byte(a.TriggerJSON), &receipt); err != nil {
			return nil, err
		}
		if !receipt.ReceiptOnly && ((m.State == "pending" && row.epoch != m.ExpectedEpoch) || (m.State == "assigned" && row.epoch != m.AssignmentEpoch)) {
			continue
		}
		occupied := row.occupied
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
		pending, err := ManagerReceiptPending(ctx, q, m)
		if err != nil {
			return nil, err
		}
		candidate := scheduler.Candidate{ManagerAuthorizationInvalid: !receipt.ReceiptOnly && !valid, Manager: &protocol.IssueManagerSpec{AgentID: contract.AgentID, OwnershipPolicy: ownershipPolicy, ReceiptOnly: receipt.ReceiptOnly, IssueID: m.IssueID, CategoryID: m.CategoryID, ExpectedAssignmentEpoch: m.ExpectedEpoch}, ManagerPin: m.Pin, ActionID: a.ActionID, TaskID: t.TaskID, Kind: a.Kind, Status: a.Status, Generation: a.Generation, TargetSpecVersion: 1, SpecVersion: 1, CreatedAt: a.CreatedAt, Lifecycle: t.Lifecycle, Control: t.ControlMode, Block: t.BlockReason, Rating: t.Rating, Weight: row.weight, DependenciesDone: true, WorkspaceOccupied: occupied || (pending && !receipt.ReceiptOnly)}
		if previous, err := store.PendingPausePredecessor(ctx, q, candidate.ActionID); err != nil {
			return nil, err
		} else if previous != "" {
			candidate.PauseContinuation = true
		}
		if !receipt.ReceiptOnly {
			if err := applyIssueAdmissionFacts(&candidate, m.IssueID, true, admission); err != nil {
				return nil, err
			}
		}
		candidate.CreatedAt = max(candidate.CreatedAt, row.todoAt)
		out = append(out, candidate)
	}
	return out, nil
}

type managerSchedulingRow struct {
	action                store.ActionRow
	task                  store.TaskRow
	manager               store.IssueManagerTask
	pin, spec             string
	epoch, todoAt, weight int64
	occupied              bool
}

func managerSchedulingRows(ctx context.Context, q store.Querier, observe func(store.WritePhase) func()) ([]managerSchedulingRow, error) {
	end := func() {}
	if observe != nil {
		end = observe(store.WriteClaimManagerQuery)
	}
	defer end()
	rows, err := q.QueryContext(ctx, `SELECT
	 a.action_id,a.task_id,a.kind,a.status,a.generation,a.created_at,a.trigger_json,
	 t.lifecycle,t.control_mode,t.block_reason,t.rating,p.weight,
	 m.issue_id,m.category_id,m.expected_epoch,COALESCE(m.assignment_epoch,0),
	 COALESCE(m.conversation_id,''),COALESCE(m.host_id,''),m.state,m.source_message_id,m.handoff_id,COALESCE(m.pin_json,''),
	 s.body_json,i.assignment_epoch,i.todo_at,
	 EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.task_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown'))
	 FROM issue_manager_tasks m JOIN plan_actions a USING(task_id)
	 JOIN tasks t USING(task_id) JOIN projects p ON p.project=t.project
	 JOIN task_specs s ON s.task_id=t.task_id AND s.spec_version=1
	 JOIN issue_controls i ON i.issue_id=m.issue_id
	 WHERE a.kind='coordinate' AND a.status='pending'
	 AND ((m.state IN ('pending','assigned') AND i.state IN ('active','cancelling') AND i.assignment_state='active')
	 OR json_extract(a.trigger_json,'$.receipt_only')=1) ORDER BY a.created_at,a.action_id`)
	if err != nil {
		return nil, err
	}
	end()
	if observe != nil {
		end = observe(store.WriteClaimManagerScan)
	}
	defer func() { end() }()
	defer rows.Close()
	var out []managerSchedulingRow
	for rows.Next() {
		var row managerSchedulingRow
		a, t, m := &row.action, &row.task, &row.manager
		if err := rows.Scan(&a.ActionID, &a.TaskID, &a.Kind, &a.Status, &a.Generation, &a.CreatedAt, &a.TriggerJSON,
			&t.Lifecycle, &t.ControlMode, &t.BlockReason, &t.Rating, &row.weight,
			&m.IssueID, &m.CategoryID, &m.ExpectedEpoch, &m.AssignmentEpoch, &m.ConversationID, &m.HostID, &m.State, &m.SourceMessageID, &m.HandoffID, &row.pin,
			&row.spec, &row.epoch, &row.todoAt, &row.occupied); err != nil {
			return nil, err
		}
		t.TaskID, m.TaskID = a.TaskID, a.TaskID
		out = append(out, row)
	}
	return out, rows.Err()
}
