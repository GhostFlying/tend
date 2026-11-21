package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func readSatisfiedPRWait(ctx context.Context, q store.Querier, run string, p protocol.WaitExternalPayload) (*protocol.ExternalWait, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM events WHERE object_type='run' AND object_id=? AND kind=? AND json_extract(body_json,'$.wait.wait_key')=? LIMIT 1`,
		run, protocol.EventPRWaitSatisfied, p.WaitKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved struct{ Wait protocol.ExternalWait }
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return nil, err
	}
	if !saved.Wait.WaitExternalPayload.Equal(p) {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "wait_key already has different content")
	}
	return &saved.Wait, nil
}

func bindPRWait(ctx context.Context, q store.Querier, v *protocol.ExternalWait) error {
	c := v.Condition
	if c == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "condition is required for a pr wait")
	}
	switch c.Kind {
	case protocol.PRWaitCheckCompleted:
		if strings.TrimSpace(c.Name) == "" || c.Name != strings.TrimSpace(c.Name) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "check_completed requires an exact check name")
		}
		if c.WorkflowRunID < 0 || c.Attempt < 0 || c.Attempt != 0 && c.WorkflowRunID == 0 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "an attempt requires its positive workflow_run_id")
		}
	case protocol.PRWaitConflictAppears, protocol.PRWaitConflictClears, protocol.PRWaitTargetMoved, protocol.PRWaitMerged, protocol.PRWaitClosed:
		if c.Name != "" || c.App != "" || c.Source != "" || c.Attempt != 0 || c.WorkflowRunID != 0 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "check selectors require check_completed")
		}
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "unsupported pull request wait condition")
	}
	item, err := store.GetIssue(ctx, q, v.IssueID)
	if err != nil {
		return err
	}
	ref, ok := findDelivery(item, v.TargetID)
	if !ok {
		return protocol.Errorf(protocol.CodeNotFound, "registered pull request delivery was removed")
	}
	v.PRBinding = &ref
	if ref.Provider != "github" && c.WorkflowRunID != 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "workflow_run_id and attempt require a GitHub delivery")
	}
	if c.Kind == protocol.PRWaitMerged || c.Kind == protocol.PRWaitClosed {
		if c.HeadSHA != "" || c.TargetRef != "" || c.TargetSHA != "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "merged and closed follow the registered PR identity")
		}
		return nil
	}
	if c.Kind == protocol.PRWaitTargetMoved {
		if c.HeadSHA != "" || c.TargetRef == "" || !validDeliverySHA(c.TargetSHA) || c.TargetRef != strings.TrimSpace(c.TargetRef) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "target_moved requires an exact target_ref and full starting target_sha")
		}
		return nil
	}
	if !validDeliverySHA(c.HeadSHA) || c.TargetSHA != "" && !validDeliverySHA(c.TargetSHA) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "this condition requires a full head_sha and an optional full target_sha")
	}
	row, err := store.GetDeliveryObservation(ctx, q, v.IssueID, v.TargetID)
	if err != nil {
		return err
	}
	o := row.Observation
	if o.CheckedAt == 0 || !validDeliverySHA(o.TargetSHA) || o.TargetRef == "" {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "the registered PR does not yet have an observed head and exact target")
	}
	if o.HeadSHA != c.HeadSHA || c.TargetSHA != "" && c.TargetSHA != o.TargetSHA || c.TargetRef != "" && c.TargetRef != o.TargetRef {
		return protocol.Errorf(protocol.CodeStaleRevision, "read the registered PR and use its current head and target")
	}
	v.ObservedTarget, v.ObservedTargetRef = o.TargetSHA, o.TargetRef
	return nil
}

func samePRWaitReference(v *protocol.ExternalWait, ref protocol.IssueDelivery) bool {
	id := v.PRBinding
	return id != nil && id.Provider == ref.Provider && id.Repository == ref.Repository && id.Number == ref.Number
}

func observePRWait(ctx context.Context, q store.Querier, v *protocol.ExternalWait) (bool, string, error) {
	if v.PREvidence != nil {
		return true, v.PREvidence.Reason, nil
	}
	item, err := store.GetIssue(ctx, q, v.IssueID)
	if err != nil {
		return false, "", err
	}
	if ref, ok := findDelivery(item, v.TargetID); !ok || !samePRWaitReference(v, ref) {
		return true, "target_unavailable", nil
	}
	row, err := store.GetDeliveryObservation(ctx, q, v.IssueID, v.TargetID)
	if err != nil {
		return false, "", err
	}
	pinPRWaitEvidence(v, row.Observation, row.FactRevision)
	if v.PREvidence == nil {
		return false, "", nil
	}
	return true, v.PREvidence.Reason, nil
}

func pinPRWaitEvidence(v *protocol.ExternalWait, o protocol.DeliveryObservation, revision int64) {
	if v.PREvidence != nil || v.Condition == nil || v.PRBinding == nil {
		return
	}
	v.ObservedState = "unknown"
	c := v.Condition
	partialMerge := o.State == "merged" && o.LineageErrorKind != "" &&
		o.ErrorKind == o.LineageErrorKind && o.CheckedAt == o.AttemptedAt
	if o.RefreshPending || o.CheckedAt == 0 || o.ErrorKind != "" && !partialMerge ||
		v.DeadlineAtMillis > 0 && o.CheckedAt > v.DeadlineAtMillis {
		return
	}
	v.ObservedRevision, v.ObservedHead, v.ObservedBase = revision, o.HeadSHA, o.BaseSHA
	e := protocol.PRWaitEvidence{Provider: v.PRBinding.Provider, Repository: v.PRBinding.Repository, Number: v.PRBinding.Number,
		FactRevision: revision, ObservedAt: o.CheckedAt, HeadSHA: o.HeadSHA, TargetRef: o.TargetRef, TargetSHA: o.TargetSHA, State: o.State, URL: o.URL}
	switch c.Kind {
	case protocol.PRWaitMerged, protocol.PRWaitClosed:
		v.ObservedState = o.State
		if o.State == c.Kind {
			e.Reason = "pull_request_" + o.State
		}
	case protocol.PRWaitTargetMoved:
		if o.TargetRef == c.TargetRef && validDeliverySHA(o.TargetSHA) {
			v.ObservedState = "unchanged"
			if o.TargetSHA != c.TargetSHA {
				e.Reason = "target_moved"
			}
		}
	default:
		if validDeliverySHA(o.HeadSHA) && o.HeadSHA != c.HeadSHA ||
			o.TargetRef != "" && o.TargetRef != v.ObservedTargetRef ||
			o.TargetRef == v.ObservedTargetRef && validDeliverySHA(o.TargetSHA) && o.TargetSHA != v.ObservedTarget {
			e.Reason = "candidate_changed"
			break
		}
		if o.HeadSHA != c.HeadSHA || o.TargetRef != v.ObservedTargetRef || o.TargetSHA != v.ObservedTarget {
			return
		}
		switch c.Kind {
		case protocol.PRWaitCheckCompleted:
			check, state := matchedPRCheck(c, o)
			v.ObservedState = state
			if check != nil {
				e.Check, e.Reason = check, "check_completed"
				if check.URL != "" {
					e.URL = check.URL
				}
			}
		case protocol.PRWaitConflictAppears:
			v.ObservedState = o.Mergeability
			if o.State == "open" && o.Mergeability == "conflicting" {
				e.Reason = c.Kind
			}
		case protocol.PRWaitConflictClears:
			v.ObservedState = o.Mergeability
			if o.State == "open" && o.Mergeability == "mergeable" {
				e.Reason = c.Kind
			}
		}
	}
	if e.Reason != "" {
		v.PREvidence = &e
		if e.Check == nil {
			v.ObservedState = o.State
		}
	}
}

func matchedPRCheck(c *protocol.PRWaitCondition, o protocol.DeliveryObservation) (*protocol.DeliveryCheck, string) {
	if o.CI == nil || o.CI.HeadSHA != c.HeadSHA {
		return nil, "unknown"
	}
	checks := o.CI.Checks
	if c.Attempt > 0 {
		checks = o.CI.AttemptChecks
	} else if o.CI.ErrorKind != "" || o.CI.Truncated {
		return nil, "unknown"
	}
	var match *protocol.DeliveryCheck
	for _, check := range checks {
		if check.Name != c.Name ||
			c.Source != "" && check.Source != "" && check.Source != c.Source ||
			c.App != "" && check.App != "" && check.App != c.App ||
			c.WorkflowRunID != 0 && check.WorkflowRunID != 0 && check.WorkflowRunID != c.WorkflowRunID ||
			c.Attempt != 0 && check.Attempt != 0 && check.Attempt != c.Attempt {
			continue
		}
		if match != nil || check.Source == "" ||
			c.Source != "" && c.Source != check.Source || c.App != "" && c.App != check.App ||
			c.WorkflowRunID != 0 && c.WorkflowRunID != check.WorkflowRunID ||
			c.Attempt != 0 && c.Attempt != check.Attempt {
			return nil, "unknown"
		}
		match = &check
	}
	if match == nil {
		return nil, "unknown"
	}
	if match.Source == "github_status_context" {
		switch match.State {
		case "success", "failure", "error":
			match.Conclusion, match.State = match.State, "completed"
			return match, match.Conclusion
		case "pending", "expected":
			return nil, "pending"
		default:
			return nil, "unknown"
		}
	}
	if oneOf(match.State, "queued", "in_progress", "waiting", "requested", "pending") {
		return nil, "pending"
	}
	if match.State != "completed" {
		return nil, "unknown"
	}
	switch match.Conclusion {
	case "success", "failure", "cancelled", "skipped", "neutral", "timed_out", "action_required", "startup_failure", "stale":
		return match, match.Conclusion
	default:
		return nil, "unknown"
	}
}

func activePRWaitIDs(ctx context.Context, q store.Querier, ref protocol.IssueDelivery) ([]string, error) {
	return store.FollowupIDs(ctx, q, `SELECT w.run_id FROM run_external_waits w
 JOIN server_delivery_references r ON r.issue_id=json_extract(w.body_json,'$.issue_id') AND r.delivery_id=json_extract(w.body_json,'$.target_id')
 WHERE w.status IN ('stopping','waiting') AND json_extract(w.body_json,'$.kind')='pr'
 AND json_extract(w.body_json,'$.pr_evidence') IS NULL AND r.provider=? AND r.repository=? AND r.number=?`,
		ref.Provider, ref.Repository, ref.Number)
}

func deliveryPRObservationNeeds(ctx context.Context, q store.Querier, ref protocol.IssueDelivery) (bool, []protocol.PRCheckAttempt, error) {
	ids, err := activePRWaitIDs(ctx, q, ref)
	if err != nil {
		return false, nil, err
	}
	checks := false
	var attempts []protocol.PRCheckAttempt
	for _, id := range ids {
		v, err := store.GetExternalWait(ctx, q, id)
		if err != nil {
			return false, nil, err
		}
		if v == nil || v.Condition == nil || !samePRWaitReference(v, ref) {
			continue
		}
		c := v.Condition
		checks = checks || c.Kind == protocol.PRWaitCheckCompleted ||
			ref.Provider == "codebase" && (c.Kind == protocol.PRWaitConflictAppears || c.Kind == protocol.PRWaitConflictClears)
		if c.Kind == protocol.PRWaitCheckCompleted && c.Attempt > 0 {
			attempt := protocol.PRCheckAttempt{HeadSHA: c.HeadSHA, Name: c.Name, WorkflowRunID: c.WorkflowRunID, Attempt: c.Attempt}
			if !slices.Contains(attempts, attempt) {
				attempts = append(attempts, attempt)
			}
		}
	}
	return checks, attempts, nil
}

// Keep the observer active for specifically requested facts even if ordinary
// observation of this PR would stop at a terminal provider state.
const prWaitForMemberSQL = `EXISTS(SELECT 1 FROM run_external_waits w
 WHERE w.status IN ('stopping','waiting') AND json_extract(w.body_json,'$.kind')='pr'
 AND json_extract(w.body_json,'$.pr_evidence') IS NULL
 AND json_extract(w.body_json,'$.issue_id')=r.issue_id AND json_extract(w.body_json,'$.target_id')=r.delivery_id
 AND json_extract(w.body_json,'$.pr_binding.provider')=r.provider
 AND json_extract(w.body_json,'$.pr_binding.repository')=r.repository
 AND json_extract(w.body_json,'$.pr_binding.number')=r.number)`

func hasPRDeliveryWait(ctx context.Context, q store.Querier, issue, delivery string) (bool, error) {
	var pending bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_delivery_references r
 WHERE r.issue_id=? AND r.delivery_id=? AND r.active=1 AND `+prWaitForMemberSQL+`)`, issue, delivery).Scan(&pending)
	return pending, err
}

// Prefer a reader that can satisfy the shared observation when an exact PR
// subscription is pending. An offline reader or different host pin does not
// remove ordinary provider visibility from an existing legacy runner.
func preferredPRDeliveryReader(ctx context.Context, q store.Querier, ref protocol.IssueDelivery, now int64) (bool, error) {
	var preferred bool
	err := q.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM server_delivery_references r
 WHERE r.provider=?1 AND r.repository=?2 AND r.number=?3 AND r.active=1 AND `+prWaitForMemberSQL+`) THEN EXISTS(
 SELECT 1 FROM server_delivery_references r
 JOIN work_items w ON w.work_item_id=r.issue_id
 JOIN json_each(w.body_json,'$.deliveries') d ON json_extract(d.value,'$.delivery_id')=r.delivery_id
 JOIN observation_jobs j ON j.issue_id=r.issue_id AND j.delivery_id=r.delivery_id AND j.status='pending'
 LEFT JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id
 JOIN host_runtime rt ON rt.host_id=COALESCE(NULLIF(json_extract(d.value,'$.host_id'),''),rt.host_id)
 JOIN hosts h ON h.host_id=rt.host_id AND h.status='active'
 WHERE r.provider=?1 AND r.repository=?2 AND r.number=?3 AND rt.online=1
 AND rt.last_heartbeat_at>?4 AND rt.last_heartbeat_at<=?5 AND json_extract(rt.runtime_json,'$.mode')='normal'
 AND COALESCE(o.query_until,0)<=?5 AND COALESCE(o.next_check_at,0)<=?5
 AND EXISTS(SELECT 1 FROM json_each(rt.runtime_json,'$.capabilities') cap WHERE cap.value=?6)
 AND EXISTS(SELECT 1 FROM json_each(rt.runtime_json,'$.capabilities') cap WHERE cap.value=?7))
 ELSE 0 END`, ref.Provider, ref.Repository, ref.Number, now-RunnerOfflineAfter.Milliseconds(), now,
		protocol.CapabilityDeliveryPRWaitEvidence, deliveryCapability(ref.Provider)).Scan(&preferred)
	return preferred, err
}

// The accepted fact and the first matching evidence commit together. A wait
// still stopping keeps the evidence until the original physical stop arrives.
func capturePRWaits(ctx context.Context, x *Exec, tx *store.Tx, ref protocol.IssueDelivery) error {
	ids, err := activePRWaitIDs(ctx, tx.Q(), ref)
	if err != nil || len(ids) == 0 {
		return err
	}
	row, err := store.GetDeliveryObservationFact(ctx, tx.Q(), ref)
	if err != nil {
		return err
	}
	var ready []string
	for _, id := range ids {
		v, err := store.GetExternalWait(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if v == nil || !samePRWaitReference(v, ref) {
			continue
		}
		previousState := v.ObservedState
		pinPRWaitEvidence(v, row.Observation, row.FactRevision)
		if v.PREvidence == nil && previousState == v.ObservedState {
			continue
		}
		if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
			return err
		}
		if v.Stopped && v.PREvidence != nil {
			ready = append(ready, v.RunID)
		}
	}
	if len(ready) > 0 {
		_, err := refreshExternalWaitsChanged(ctx, x, tx, nil, ready...)
		return err
	}
	return nil
}
