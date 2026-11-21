package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const deliveryMergeVerifiedEvent = "issue.delivery_merge_verified"

// A merge is a business input only when it verifies the original authorized
// merge-only execution. Ordinary provider observations never open such a duty.
func deliveryMergeEvents(ctx context.Context, x *Exec, tx *store.Tx, ref protocol.IssueDelivery, state string) ([]Event, error) {
	if state != "merged" {
		return nil, nil
	}
	fact, err := store.GetDeliveryObservationFact(ctx, tx.Q(), ref)
	if err != nil {
		return nil, err
	}
	o := fact.Observation
	if o.ErrorKind != "" || o.RefreshPending || o.State != "merged" || o.CheckedAt == 0 ||
		x.serverDeliveryObservations && o.Observer != "server" {
		return nil, nil
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT d.issue_id,d.task_id
 FROM server_delivery_references r JOIN issue_delivery_duties d
 ON d.issue_id=r.issue_id AND json_extract(d.body_json,'$.candidate.delivery_id')=r.delivery_id
 JOIN issue_delivery_executions e ON e.issue_id=d.issue_id AND e.source_task_id=d.task_id AND e.duty_revision=d.revision
 AND e.task_id=json_extract(d.body_json,'$.execution_task_id')
 JOIN tasks t ON t.task_id=e.task_id AND (t.lifecycle='completed' OR t.kind='issue_manager')
 WHERE r.provider=? AND r.repository=? AND r.number=? AND d.status='executing'
 AND json_extract(d.body_json,'$.candidate.deploy')=0 AND json_extract(d.body_json,'$.candidate.head_sha')=?
 AND NOT EXISTS (SELECT 1 FROM events ev WHERE ev.object_type='issue' AND ev.object_id=d.issue_id AND ev.kind=?
 AND json_extract(ev.body_json,'$.task_id')=d.task_id AND json_extract(ev.body_json,'$.duty_revision')=d.revision)`,
		ref.Provider, ref.Repository, ref.Number, o.HeadSHA, deliveryMergeVerifiedEvent)
	if err != nil {
		return nil, err
	}
	type dutyRef struct{ issue, task string }
	var matches []dutyRef
	for rows.Next() {
		var match dutyRef
		if err := rows.Scan(&match.issue, &match.task); err != nil {
			rows.Close()
			return nil, err
		}
		matches = append(matches, match)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var events []Event
	for _, match := range matches {
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), match.issue)
		if err != nil {
			return nil, err
		}
		duty, err := store.GetIssueDeliveryDuty(ctx, tx.Q(), match.issue, match.task)
		if err != nil {
			return nil, err
		}
		if duty.AssignmentEpoch != issue.AssignmentEpoch || duty.Candidate == nil ||
			duty.Candidate.MergeSHA != "" && duty.Candidate.MergeSHA != o.MergeSHA {
			continue
		}
		if _, err := deliveryDeploymentSource(*duty.Candidate, o); err != nil {
			continue
		}
		if err := store.DeliveryGrantValid(ctx, tx.Q(), issue, duty); err != nil {
			if knownDeliveryRestriction(err) {
				continue
			}
			return nil, err
		}
		if duty.Authorization != nil {
			_, policy, err := store.EffectiveIssuePRPolicy(ctx, tx.Q(), issue)
			if err != nil {
				return nil, err
			}
			if policy != nil {
				if err := store.RequireDeliveryDutyReview(ctx, tx.Q(), issue, duty); err != nil {
					if knownDeliveryRestriction(err) {
						continue
					}
					return nil, err
				}
			}
		}
		if _, err := completedTaskEvidence(ctx, tx.Q(), duty.ExecutionTaskID, duty.Reason); err != nil {
			if knownDeliveryRestriction(err) {
				continue
			}
			return nil, err
		}
		events = append(events, Event{ObjectType: "issue", ObjectID: match.issue, Kind: deliveryMergeVerifiedEvent,
			Body: map[string]any{"task_id": duty.TaskID, "duty_revision": duty.Revision, "execution_task_id": duty.ExecutionTaskID,
				"delivery_id": duty.Candidate.DeliveryID, "fact_revision": fact.FactRevision, "head_sha": o.HeadSHA, "merge_sha": o.MergeSHA, "url": o.URL}})
	}
	return events, nil
}

func knownDeliveryRestriction(err error) bool {
	var domain *protocol.Error
	return errors.As(err, &domain) || errors.Is(err, store.ErrNotFound)
}
