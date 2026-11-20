package views

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type boardPlacement struct {
	column protocol.IssueBoardColumn
	hint   protocol.IssueBoardWaitHint
}

func executionBoardPage(ctx context.Context, q store.Querier, f CanonicalIssueFilter, out protocol.IssuePage, base string, args []any, facts executionFacts) (protocol.IssuePage, error) {
	out.ColumnCounts = make(map[protocol.IssueBoardColumn]int)
	for _, column := range protocol.IssueBoardColumnValues() {
		out.ColumnCounts[column] = 0
	}
	queued, err := facts.boardQueuedIssues(ctx, q)
	if err != nil {
		return out, err
	}
	roots, err := boardIssueControls(ctx, q, base, args)
	if err != nil {
		return out, err
	}
	for _, control := range roots {
		id := control.IssueID
		placement, err := boardPlacementForIssue(ctx, q, control, queued[id])
		if err != nil {
			return out, err
		}
		out.ColumnCounts[placement.column]++
		if id <= f.After || f.BoardColumn != "all" && f.BoardColumn != placement.column {
			continue
		}
		if len(out.Items) == f.Limit {
			out.More = true
			out.Next = out.Items[len(out.Items)-1].IssueID
			continue
		}
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return out, err
		}
		card, err := issueBoardCard(ctx, q, issue, facts)
		if err != nil {
			return out, err
		}
		card.Column, card.WaitHint = placement.column, placement.hint
		out.Items = append(out.Items, boardIssueSummary(issue, card))
	}
	out.FactsSeq, err = store.LatestEventSeq(ctx, q)
	return out, err
}

func boardIssueControls(ctx context.Context, q store.Querier, base string, args []any) ([]protocol.IssueControl, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.issue_id,c.state,c.work_status,c.assignment_state,c.todo_at`+base+` ORDER BY c.issue_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var controls []protocol.IssueControl
	for rows.Next() {
		var issue protocol.IssueControl
		if err := rows.Scan(&issue.IssueID, &issue.State, &issue.WorkStatus, &issue.AssignmentState, &issue.TodoAt); err != nil {
			return nil, err
		}
		controls = append(controls, issue)
	}
	return controls, rows.Err()
}

// Reuse the scheduler's selected candidates. A task projection that only says
// preparation is blocked does not by itself prove there is a queued action.
func (f executionFacts) boardQueuedIssues(ctx context.Context, q store.Querier) (map[string]bool, error) {
	queued := make(map[string]bool)
	for id := range f.issues {
		queued[id] = true
	}
	for id, candidate := range f.candidates {
		if candidate.Control != protocol.ControlManaged || candidate.Lifecycle.Terminal() {
			continue
		}
		if candidate.Manager != nil {
			if f.ownerActions[id] == protocol.ExecutionQueued {
				queued[candidate.Manager.IssueID] = true
			}
			continue
		}
		var issueID string
		err := q.QueryRowContext(ctx, `SELECT COALESCE(CASE WHEN ?='accept' THEN c.review_issue_id END,o.work_item_id,'')
 FROM tasks t LEFT JOIN task_execution_contracts c USING(task_id) LEFT JOIN task_work_item_owners o USING(task_id)
 WHERE t.task_id=?`, candidate.Kind, id).Scan(&issueID)
		if err != nil {
			return nil, err
		}
		if issueID != "" {
			queued[issueID] = true
		}
	}
	return queued, nil
}

func boardPlacementForIssue(ctx context.Context, q store.Querier, issue protocol.IssueControl, queued bool) (boardPlacement, error) {
	switch issue.BoardStatus() {
	case protocol.IssueDone:
		return boardPlacement{column: protocol.IssueColumnDone}, nil
	case protocol.IssueWorkCancelled:
		return boardPlacement{column: protocol.IssueColumnCancelled}, nil
	}
	runs, err := boardRunFacts(ctx, q, issue.IssueID)
	if err != nil {
		return boardPlacement{}, err
	}
	if runs.live {
		return boardPlacement{column: protocol.IssueColumnWIP}, nil
	}
	if issue.BoardStatus() == protocol.IssueBacklog {
		return boardPlacement{column: protocol.IssueColumnBacklog}, nil
	}
	mayWait := !runs.unsettled && issue.AssignmentState == "active" && issue.State == protocol.IssueActive
	if mayWait {
		decision, err := boardHasOpenOwnerDecision(ctx, q, issue.IssueID)
		if err != nil {
			return boardPlacement{}, err
		}
		if decision {
			return boardPlacement{protocol.IssueColumnWaiting, protocol.IssueBoardWaitDecision}, nil
		}
	}
	checksPending, checksPreviouslyWaited, err := boardCheckWaits(ctx, q, runs)
	if err != nil {
		return boardPlacement{}, err
	}
	if mayWait {
		if checksPending {
			return boardPlacement{protocol.IssueColumnWaiting, protocol.IssueBoardWaitCI}, nil
		}
		if runs.externalWait || queued {
			return boardPlacement{column: protocol.IssueColumnWaiting}, nil
		}
	}

	// A stopped wait can still explain the last placement after its condition
	// clears. Only current proof above can produce a hint.
	switch {
	case !runs.startedUnsettled && (runs.lastWait || checksPreviouslyWaited):
		return boardPlacement{column: protocol.IssueColumnWaiting}, nil
	case runs.lastStart > 0:
		return boardPlacement{column: protocol.IssueColumnWIP}, nil
	case issue.TodoAt > 0:
		return boardPlacement{column: protocol.IssueColumnWaiting}, nil
	default:
		return boardPlacement{column: protocol.IssueColumnBacklog}, nil
	}
}

type boardCheckWait struct {
	RunID     string `json:"run_id"`
	StoppedAt int64  `json:"stopped_at"`
	EndedAt   *int64 `json:"ended_at"`
	Current   bool   `json:"current"`
}

type issueBoardRuns struct {
	live, unsettled, startedUnsettled, lastWait, externalWait bool
	lastStart, lastSettled                                    int64
	checks                                                    []boardCheckWait
}

func boardRunFacts(ctx context.Context, q store.Querier, issueID string) (issueBoardRuns, error) {
	var f issueBoardRuns
	var checks string
	err := q.QueryRowContext(ctx, boardRunFactsSQL, issueID).Scan(&f.live, &f.unsettled, &f.startedUnsettled, &f.lastStart, &f.lastSettled, &f.lastWait, &f.externalWait, &checks)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal([]byte(checks), &f.checks)
}

func boardCheckWaits(ctx context.Context, q store.Querier, runs issueBoardRuns) (pending, previouslyWaited bool, err error) {
	for _, check := range runs.checks {
		if check.EndedAt != nil {
			// A head change or terminal observation closes this exact wait's
			// interval even if the provider later starts another cycle.
			quietAt := max(check.StoppedAt, runs.lastStart, runs.lastSettled)
			// Equal timestamps leave the order of unrelated wall-clock receipts unknown.
			previouslyWaited = previouslyWaited || quietAt < *check.EndedAt
			continue
		}
		if !check.Current {
			continue
		}
		wait, err := store.GetExternalWait(ctx, q, check.RunID)
		if err != nil {
			return false, false, err
		}
		if wait == nil || wait.ResumeReason != "" {
			continue
		}
		row, err := store.GetDeliveryObservation(ctx, q, wait.IssueID, wait.TargetID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, false, err
		}
		ready, _ := store.ApplyChecksWaitObservation(wait, row)
		pending = pending || !ready
		previouslyWaited = previouslyWaited || !ready
	}
	return pending, previouslyWaited, nil
}

// Use the same responsibility precedence as issueCounts. Explicit Run pins
// supersede task ownership; old manager carriers also require the original
// host and any recorded Run pin to agree. An old unsettled lease can fence a
// new wait without making the old assignee the current executing owner.
const boardRunFactsSQL = `
WITH issue AS (SELECT * FROM issue_controls WHERE issue_id=?),
related(run_id) AS (
 SELECT p.run_id FROM issue i JOIN run_issue_responsibilities p ON p.issue_id=i.issue_id
 UNION
 SELECT r.run_id FROM issue i JOIN issue_manager_tasks m ON m.issue_id=i.issue_id JOIN runs r USING(task_id)
 UNION
 SELECT r.run_id FROM issue i JOIN task_work_item_owners o ON o.work_item_id=i.issue_id JOIN runs r USING(task_id)
 UNION
 SELECT r.run_id FROM issue i JOIN task_execution_contracts c ON c.review_issue_id=i.issue_id JOIN runs r ON r.task_id=c.task_id AND r.role='accept'
),
owned AS (
 SELECT r.*,l.status AS lease_status,l.updated_at AS lease_updated_at,i.todo_at,
   i.assignment_state='active' AND
   (p.run_id IS NULL OR (p.assignment_epoch=i.assignment_epoch AND p.agent_ref=i.assignee_ref)) AND
   (r.role<>'issue_manager' OR (m.state='assigned' AND m.assignment_epoch=i.assignment_epoch AND
     i.assignee_kind='agent' AND m.host_id=r.host_id AND
     json_extract(m.pin_json,'$.identity.execution_environment_id') || '/' || json_extract(m.pin_json,'$.identity.local_id')=i.assignee_ref AND
     (e.run_id IS NULL OR json_extract(e.pin_json,'$.identity.execution_environment_id') || '/' || json_extract(e.pin_json,'$.identity.local_id')=i.assignee_ref)
   )) AS current_owner
 FROM issue i CROSS JOIN related x JOIN runs r USING(run_id)
 LEFT JOIN leases l USING(run_id) LEFT JOIN run_issue_responsibilities p USING(run_id)
 LEFT JOIN issue_manager_tasks m USING(task_id) LEFT JOIN run_agent_executions e USING(run_id)
 LEFT JOIN task_work_item_owners o USING(task_id) LEFT JOIN task_execution_contracts c USING(task_id)
 WHERE COALESCE(p.issue_id, CASE WHEN r.role='issue_manager' THEN m.issue_id WHEN r.role='accept' THEN c.review_issue_id END,o.work_item_id)=i.issue_id
),
run_times AS (
 SELECT r.*,
   CASE WHEN r.created_at>=r.todo_at THEN MAX(
     CASE WHEN r.observed_state IN ('running','waiting') THEN COALESCE(r.last_confirmed_at,r.created_at) ELSE 0 END,
     COALESCE((SELECT MAX(e.created_at) FROM events e
       WHERE e.object_type='run' AND e.object_id=r.run_id AND e.kind='run.manager_started'),0),
     COALESCE((SELECT MAX(e.created_at) FROM events e
       WHERE e.object_type='task' AND e.object_id=r.task_id AND e.kind='run.started'
         AND json_extract(e.body_json,'$.run_id')=r.run_id),0)
   ) ELSE 0 END AS started_at,
   CASE WHEN r.observed_state IN ('finished','failed') AND r.lease_status='released'
     THEN r.lease_updated_at ELSE 0 END AS stopped_at,
   r.observed_state<>'starting' AND
     (r.observed_state NOT IN ('finished','failed') OR COALESCE(r.lease_status,'') IN ('active','lapsed')) AS unsettled
 FROM owned r
),
activity AS (
 SELECT COALESCE(MAX(started_at),0) AS last_start,
        COALESCE(MAX(CASE WHEN started_at>0 THEN stopped_at ELSE 0 END),0) AS last_settled
 FROM run_times WHERE current_owner
),
waits AS (
 SELECT r.*,
   r.observed_state='finished' AND json_extract(w.body_json,'$.stopped')=1 AND COALESCE(wait_successor.started_at,0)=0 AS stopped_wait,
   h.stopped=1 AND COALESCE(question_successor.started_at,0)=0 AS stopped_question,
   w.status='waiting' AND w.successor_run_id IS NULL AND a.status='dispatched' AND a.target_spec_version=t.spec_version AND
     t.lifecycle NOT IN ('completed','cancelled') AND t.control_mode='managed' AND
     (r.role<>'implement' OR a.generation=t.implement_generation) AND
     (r.role<>'accept' OR a.target_artifact_id=t.current_artifact_id) AS current_wait,
   CASE w.status WHEN 'continued' THEN continuation.created_at WHEN 'cancelled' THEN w.next_check_at END AS wait_ended_at,
   CASE WHEN d.status='open' THEN NULL ELSE d.updated_at END AS question_ended_at,
   json_type(w.body_json,'$.board_checks')='object' AS checks_tracked,
   json_extract(w.body_json,'$.board_checks.invalidated_at') AS checks_invalidated_at,
   json_extract(w.body_json,'$.kind') AS wait_kind
 FROM run_times r
 LEFT JOIN run_external_waits w USING(run_id) LEFT JOIN run_question_handoffs h USING(run_id)
 LEFT JOIN decisions d ON d.decision_id=h.decision_id
 LEFT JOIN run_times wait_successor ON wait_successor.run_id=w.successor_run_id
 LEFT JOIN run_times question_successor ON question_successor.run_id=h.successor_run_id
 LEFT JOIN plan_actions continuation ON continuation.action_id=w.continuation_action_id
 JOIN plan_actions a USING(action_id) JOIN tasks t ON t.task_id=r.task_id
 WHERE r.current_owner AND r.created_at>=r.todo_at AND r.stopped_at>0
),
effective_waits AS (
 SELECT w.*,MAX(w.stopped_at, a.last_start, a.last_settled) AS quiet_at,
   CASE WHEN w.checks_tracked THEN COALESCE(w.checks_invalidated_at,w.wait_ended_at) END AS checks_ended_at
 FROM waits w CROSS JOIN activity a
)
SELECT
 EXISTS(SELECT 1 FROM run_times WHERE current_owner AND observed_state IN ('running','waiting') AND lease_status='active'),
 EXISTS(SELECT 1 FROM run_times WHERE unsettled),
 EXISTS(SELECT 1 FROM run_times WHERE unsettled AND started_at>0),
 last_start,
 last_settled,
 EXISTS(SELECT 1 FROM effective_waits WHERE
   (stopped_question AND (question_ended_at IS NULL OR quiet_at<=question_ended_at)) OR
   (stopped_wait AND wait_kind<>'checks' AND (current_wait OR quiet_at<=wait_ended_at))),
 EXISTS(SELECT 1 FROM effective_waits WHERE stopped_wait AND current_wait AND wait_kind<>'checks'),
 (SELECT json_group_array(json_object('run_id', run_id, 'stopped_at', stopped_at,
    'ended_at', checks_ended_at, 'current', json(CASE WHEN current_wait THEN 'true' ELSE 'false' END)))
   FROM effective_waits WHERE stopped_wait AND wait_kind='checks' AND (current_wait OR quiet_at<=checks_ended_at))
FROM activity
`

func boardHasOpenOwnerDecision(ctx context.Context, q store.Querier, issueID string) (bool, error) {
	var open bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM attentions a LEFT JOIN attention_responsibilities r USING(attention_id)
 WHERE a.attention_id IN (`+responsibleAttentionIDsSQL+`) AND a.kind='decision' AND a.status='open'
   AND COALESCE(r.recipient_kind,'owner')='owner')`, issueID, issueID).Scan(&open)
	return open, err
}

func validExecutionBoardColumn(column protocol.IssueBoardColumn) bool {
	return column == "all" || slices.Contains(protocol.IssueBoardColumnValues(), column)
}
