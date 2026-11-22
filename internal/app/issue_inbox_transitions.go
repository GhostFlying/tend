package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func recordIssueEndpointInInbox(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue) error {
	var title, closureKind string
	switch issue.State {
	case protocol.IssueClosed:
		title = "Issue ended"
		if issue.Closure != nil {
			closureKind = string(issue.Closure.Kind)
			if issue.Closure.Kind == protocol.WorkItemClosureCompleted {
				title = "Issue completed"
			}
		}
	case protocol.IssueCancelled:
		title = "Issue cancelled"
	default:
		return nil
	}
	_, _, err := recordInboxTransition(ctx, x, tx, issue.IssueID, title, protocol.IssueInboxTransition{
		State: string(issue.State), IssueRevision: issue.Revision, ClosureKind: closureKind,
	}, "endpoint:"+strconv.FormatInt(issue.Revision, 10))
	return err
}

func recordInboxTransition(ctx context.Context, x *Exec, tx *store.Tx, issue, title string, transition protocol.IssueInboxTransition, origin string) (string, bool, error) {
	id := protocol.NativeObjectID("attn", "issue-inbox:"+issue, origin)
	var exists bool
	if err := tx.Q().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM attentions WHERE attention_id=?)", id).Scan(&exists); err != nil || exists {
		return id, false, err
	}
	now := x.Now.UnixMilli()
	a := protocol.Attention{
		AttentionID: id, IssueID: issue, Kind: "information", Status: "open",
		Title: title, Revision: 1, RaisedBy: protocol.Principal{Kind: protocol.PrincipalSystem},
		CreatedAt: now, UpdatedAt: now, IssueTransition: &transition,
	}
	if err := tx.SaveAttention(ctx, a, 0, x.Env.RequestID); err != nil {
		return "", false, err
	}
	// A durable Inbox fact invalidates live reads without entering the
	// attention.changed path that publishes messages and wakes agents.
	_, err := tx.InsertEvent(ctx, store.EventRow{
		EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "attention",
		ObjectID: id, ObjectVersion: 1, Kind: "attention.issue_transition", BodyJSON: "{}",
		RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: now,
	})
	return id, err == nil, err
}

// A refresh may publish a new notice after confirming the Issue has stopped
// for CI. On physical stop, a wait may also join a notice that is already live.
func recordChecksWaitInInbox(ctx context.Context, x *Exec, tx *store.Tx, wait *protocol.ExternalWait, publish bool) (bool, error) {
	if wait.Kind != "checks" && (wait.Kind != "pr" || wait.Condition == nil || wait.Condition.Kind != protocol.PRWaitCheckCompleted) {
		return false, nil
	}
	q := tx.Q()
	var linked sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT inbox_attention_id FROM run_external_waits WHERE run_id=?`, wait.RunID).Scan(&linked); err != nil || linked.Valid {
		return false, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, wait.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if issue.State != protocol.IssueActive || issue.AssignmentState != "active" || issue.BoardStatus() == protocol.IssueBacklog {
		return false, nil
	}
	index := slices.IndexFunc(issue.Plan.Deliveries, func(ref protocol.IssueDelivery) bool {
		return ref.DeliveryID == wait.TargetID
	})
	if index < 0 {
		return false, nil
	}
	delivery := issue.Plan.Deliveries[index]
	row, err := store.GetDeliveryObservation(ctx, q, wait.IssueID, wait.TargetID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	o := row.Observation
	if o.State != "open" || o.HeadSHA == "" || o.RefreshPending || o.ErrorKind != "" ||
		o.CI == nil || o.CI.HeadSHA != o.HeadSHA || o.CI.Truncated || o.CI.ErrorKind != "" ||
		!waitingForRequiredCheck(wait, delivery, o) {
		return false, nil
	}

	overlaps, id, err := inboxChecksWaitOverlaps(ctx, q, issue, delivery, o, x.Now.UnixMilli())
	if err != nil || !slices.Contains(overlaps, wait.RunID) {
		return false, err
	}
	var created bool
	if id == "" {
		if !publish {
			return false, nil
		}
		var busy bool
		if err := q.QueryRowContext(ctx, issueInboxUnsettledRunSQL, issue.IssueID, issue.IssueID, issue.IssueID, issue.IssueID, issue.IssueID).Scan(&busy); err != nil || busy {
			return false, err
		}
		id, created, err = recordInboxTransition(ctx, x, tx, issue.IssueID, "Waiting for CI", protocol.IssueInboxTransition{
			State: "waiting_ci", WaitID: wait.WaitID, RunID: wait.RunID, HeadSHA: o.HeadSHA, Delivery: &delivery,
		}, "checks:"+wait.WaitID)
		if err != nil {
			return false, err
		}
	}
	// Bind all already stopped participants even when the sweep only visits one.
	// The notification remains shared when its representative resumes first.
	for _, runID := range overlaps {
		if _, err := tx.Exec(ctx, `UPDATE run_external_waits SET inbox_attention_id=? WHERE run_id=? AND inbox_attention_id IS NULL`, id, runID); err != nil {
			return false, err
		}
	}
	return created, nil
}

func waitingForRequiredCheck(wait *protocol.ExternalWait, delivery protocol.IssueDelivery, o protocol.DeliveryObservation) bool {
	if wait.Kind == "checks" {
		if !oneOf(o.CI.State, "pending", "expected", "running", "queued", "in_progress") {
			return false
		}
		return slices.ContainsFunc(o.CI.Checks, protocol.DeliveryCheck.RequiredAndPending)
	}
	c := wait.Condition
	if c == nil || c.Kind != protocol.PRWaitCheckCompleted || wait.PREvidence != nil || !samePRWaitReference(wait, delivery) ||
		c.HeadSHA != o.HeadSHA || wait.ObservedTargetRef != o.TargetRef || wait.ObservedTarget != o.TargetSHA {
		return false
	}
	check := selectPRCheck(c, o)
	return check != nil && check.RequiredAndPending()
}

func inboxChecksWaitOverlaps(ctx context.Context, q store.Querier, issue protocol.Issue, delivery protocol.IssueDelivery, o protocol.DeliveryObservation, now int64) ([]string, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT w.run_id,w.body_json,w.inbox_attention_id FROM run_external_waits w
 JOIN runs r USING(run_id) JOIN leases l USING(run_id) JOIN tasks t ON t.task_id=w.task_id
 JOIN plan_actions a ON a.action_id=r.action_id LEFT JOIN run_issue_responsibilities pin USING(run_id)
 LEFT JOIN issue_manager_tasks m ON m.task_id=r.task_id
 LEFT JOIN task_work_item_owners own ON own.task_id=r.task_id
 LEFT JOIN task_execution_contracts c ON c.task_id=r.task_id
 WHERE w.status='waiting' AND w.successor_run_id IS NULL
   AND json_extract(w.body_json,'$.issue_id')=? AND json_extract(w.body_json,'$.target_id')=?
   AND (json_extract(w.body_json,'$.board_checks.head')=? OR
        (json_extract(w.body_json,'$.kind')='pr' AND json_extract(w.body_json,'$.condition.head_sha')=?))
   AND json_extract(w.body_json,'$.board_checks.invalidated_at') IS NULL
   AND COALESCE(json_extract(w.body_json,'$.resume_reason'),'')=''
   AND (COALESCE(json_extract(w.body_json,'$.next_reminder_at_ms'),0)=0 OR json_extract(w.body_json,'$.next_reminder_at_ms')>?)
   AND `+issueInboxRunOwnerSQL+`=json_extract(w.body_json,'$.issue_id')
   AND r.observed_state='finished' AND l.status='released'
   AND (pin.run_id IS NULL OR (pin.assignment_epoch=? AND pin.agent_ref=?))
   AND t.control_mode='managed' AND t.lifecycle NOT IN ('completed','cancelled')
   AND a.status='dispatched' AND a.target_spec_version=t.spec_version
   AND (a.kind<>'implement' OR a.generation=t.implement_generation)
   AND (a.kind<>'accept' OR a.target_artifact_id=t.current_artifact_id)
 ORDER BY w.rowid`, issue.IssueID, delivery.DeliveryID, o.HeadSHA, o.HeadSHA, now, issue.AssignmentEpoch, issue.Assignee.Ref)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var ids []string
	var id string
	for rows.Next() {
		var runID, raw string
		var linked sql.NullString
		if err := rows.Scan(&runID, &raw, &linked); err != nil {
			return nil, "", err
		}
		var other protocol.ExternalWait
		if err := json.Unmarshal([]byte(raw), &other); err != nil {
			return nil, "", err
		}
		if !waitingForRequiredCheck(&other, delivery, o) {
			continue
		}
		ids = append(ids, runID)
		if id == "" && linked.Valid {
			id = linked.String
		}
	}
	return ids, id, rows.Err()
}

// Explicit Run responsibility takes precedence over task associations, as in the Issue execution board.
const issueInboxRunOwnerSQL = `COALESCE(pin.issue_id, CASE WHEN r.role='issue_manager' THEN m.issue_id WHEN r.role='accept' THEN c.review_issue_id END, own.work_item_id)`

// A live or unsettled Run fences an Issue-level wait.
const issueInboxUnsettledRunSQL = `
WITH related(run_id) AS (
 SELECT run_id FROM run_issue_responsibilities WHERE issue_id=?
 UNION SELECT r.run_id FROM runs r JOIN issue_manager_tasks m USING(task_id) WHERE m.issue_id=?
 UNION SELECT r.run_id FROM runs r JOIN task_work_item_owners o USING(task_id) WHERE o.work_item_id=?
 UNION SELECT r.run_id FROM runs r JOIN task_execution_contracts c USING(task_id) WHERE c.review_issue_id=? AND r.role='accept'
)
SELECT EXISTS(SELECT 1 FROM related JOIN runs r USING(run_id) LEFT JOIN leases l USING(run_id)
 LEFT JOIN run_issue_responsibilities pin USING(run_id)
 LEFT JOIN issue_manager_tasks m ON m.task_id=r.task_id
 LEFT JOIN task_work_item_owners own ON own.task_id=r.task_id
 LEFT JOIN task_execution_contracts c ON c.task_id=r.task_id
 WHERE ` + issueInboxRunOwnerSQL + `=?
 AND r.observed_state<>'starting'
 AND (r.observed_state NOT IN ('finished','failed') OR COALESCE(l.status,'') IN ('active','lapsed')))`
