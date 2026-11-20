package app

import (
	"context"
	"database/sql"
	"errors"
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

// Existing and newly stopped waits both enter through external wait refresh.
// The caller has established stop, a current action and no continuation cause.
func recordChecksWaitInInbox(ctx context.Context, x *Exec, tx *store.Tx, wait *protocol.ExternalWait) (bool, error) {
	if wait.Kind != "checks" || wait.ObservedHead == "" {
		return false, nil
	}
	q := tx.Q()
	var own sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT inbox_attention_id FROM run_external_waits WHERE run_id=?`, wait.RunID).Scan(&own); err != nil || own.Valid {
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
	row, err := store.GetDeliveryObservation(ctx, q, wait.IssueID, wait.TargetID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	o := row.Observation
	if o.State != "open" || o.HeadSHA != wait.ObservedHead || o.RefreshPending || o.ErrorKind != "" ||
		o.CI == nil || o.CI.HeadSHA != o.HeadSHA || o.CI.Truncated || o.CI.ErrorKind != "" ||
		!oneOf(o.CI.State, "pending", "expected", "running", "queued", "in_progress") {
		return false, nil
	}
	var busy bool
	if err := q.QueryRowContext(ctx, issueInboxUnsettledRunSQL, issue.IssueID, issue.IssueID, issue.IssueID, issue.IssueID, issue.IssueID).Scan(&busy); err != nil || busy {
		return false, err
	}

	// A second wait that is already overlapping the active episode shares its
	// entry. Once the episode settles, a later wait receives its own identity.
	var id string
	err = q.QueryRowContext(ctx, `SELECT w.inbox_attention_id FROM run_external_waits w
 JOIN runs r USING(run_id) JOIN leases l USING(run_id) JOIN tasks t ON t.task_id=w.task_id
 JOIN plan_actions a ON a.action_id=r.action_id LEFT JOIN run_issue_responsibilities pin USING(run_id)
 WHERE w.status='waiting' AND w.inbox_attention_id IS NOT NULL AND w.successor_run_id IS NULL
   AND json_extract(w.body_json,'$.issue_id')=? AND json_extract(w.body_json,'$.target_id')=?
   AND json_extract(w.body_json,'$.observed_head')=? AND COALESCE(json_extract(w.body_json,'$.resume_reason'),'')=''
   AND r.observed_state='finished' AND l.status='released'
   AND (pin.run_id IS NULL OR (pin.assignment_epoch=? AND pin.agent_ref=?))
   AND t.control_mode='managed' AND t.lifecycle NOT IN ('completed','cancelled')
   AND a.status='dispatched' AND a.target_spec_version=t.spec_version
   AND (a.kind<>'implement' OR a.generation=t.implement_generation)
   AND (a.kind<>'accept' OR a.target_artifact_id=t.current_artifact_id)
 ORDER BY w.rowid LIMIT 1`, issue.IssueID, wait.TargetID, o.HeadSHA, issue.AssignmentEpoch, issue.Assignee.Ref).Scan(&id)
	created := false
	if errors.Is(err, sql.ErrNoRows) {
		var delivery protocol.IssueDelivery
		for _, ref := range issue.Plan.Deliveries {
			if ref.DeliveryID == wait.TargetID {
				delivery = ref
				break
			}
		}
		if delivery.DeliveryID == "" {
			return false, nil
		}
		id, created, err = recordInboxTransition(ctx, x, tx, issue.IssueID, "Waiting for CI", protocol.IssueInboxTransition{
			State: "waiting_ci", WaitID: wait.WaitID, RunID: wait.RunID, HeadSHA: o.HeadSHA, Delivery: &delivery,
		}, "checks:"+wait.WaitID)
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE run_external_waits SET inbox_attention_id=? WHERE run_id=?`, id, wait.RunID)
	return created, err
}

// Explicit Run responsibility takes precedence over the task associations,
// as in the Issue execution board. A live or unsettled run fences an Issue-level wait.
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
 LEFT JOIN task_work_item_owners o ON o.task_id=r.task_id
 LEFT JOIN task_execution_contracts c ON c.task_id=r.task_id
 WHERE COALESCE(pin.issue_id, CASE WHEN r.role='issue_manager' THEN m.issue_id WHEN r.role='accept' THEN c.review_issue_id END, o.work_item_id)=?
 AND r.observed_state<>'starting'
 AND (r.observed_state NOT IN ('finished','failed') OR COALESCE(l.status,'') IN ('active','lapsed')))`
