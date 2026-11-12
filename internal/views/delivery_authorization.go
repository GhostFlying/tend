package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DeliveryExecutionAuthority reads only the binding of task. It does not accept
// a claimed source task, candidate, approval or policy from the reader.
func DeliveryExecutionAuthority(ctx context.Context, q store.Querier, task string) (*protocol.DeliveryExecutionAuthority, error) {
	v := &protocol.DeliveryExecutionAuthority{Source: "server.issue_delivery_executions", TaskID: task}
	var err error
	v.History, v.HistoryMore, err = deliveryExecutionHistoryIndex(ctx, q, task)
	if err != nil {
		return nil, err
	}
	var raw string
	err = q.QueryRowContext(ctx, `SELECT issue_id,source_task_id,duty_revision,candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task).
		Scan(&v.IssueID, &v.SourceTaskID, &v.DutyRevision, &raw, &v.UpgradeID)
	if errors.Is(err, sql.ErrNoRows) {
		if len(v.History) > 0 {
			v.InvalidReason = "no_current_delivery_execution; select the Task or original Run and duty_revision to read historical facts"
			return v, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var frozen struct {
		protocol.IssueDeliveryCandidate
		AssignmentEpoch int64                        `json:"assignment_epoch"`
		Authorization   *protocol.IssueDeliveryGrant `json:"authorization,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return nil, err
	}
	v.Candidate, v.AssignmentEpoch, v.Grant = frozen.IssueDeliveryCandidate, frozen.AssignmentEpoch, frozen.Authorization
	row, err := store.GetTask(ctx, q, task)
	if err != nil {
		return nil, err
	}
	v.SpecVersion, v.ControlRevision = row.SpecVersion, row.ControlRevision
	issue, err := store.GetCanonicalIssue(ctx, q, v.IssueID)
	if err != nil {
		return nil, err
	}
	v.CurrentAssignmentEpoch = issue.AssignmentEpoch
	policyIssue, policy, err := store.EffectiveIssuePRPolicy(ctx, q, issue)
	if err != nil {
		return nil, err
	}
	if policy != nil {
		v.Policy = &protocol.ObservedDeliveryPolicy{IssueID: policyIssue.IssueID, IssueRevision: policyIssue.Revision, Mode: policy.Mode, Repository: policy.Repository, Digest: store.IssuePRPolicyDigest(policy), Deployment: policy.Deployment}
		if authority := policy.Authority; authority != nil {
			v.Policy.RequestID, v.Policy.SourceMessageID, v.Policy.SourceRevision = authority.RequestID, authority.SourceMessageID, authority.SourceRevision
		}
	}
	duty, err := store.GetIssueDeliveryDuty(ctx, q, v.IssueID, v.SourceTaskID)
	if err != nil {
		return nil, err
	}
	v.CurrentDutyRevision, v.DutyStatus = duty.Revision, duty.Status
	v.Settlement, err = store.DeliveryDeploymentSettlement(ctx, q, task, 0)
	if err != nil && protocol.CodeOf(err) == protocol.CodeInternal {
		return nil, err
	}
	if duty.AttentionID != "" {
		attention, err := store.GetAttention(ctx, q, duty.AttentionID)
		if err != nil {
			return nil, err
		}
		decision, err := store.GetDecision(ctx, q, attention.DecisionID)
		if err != nil {
			return nil, err
		}
		approval := &protocol.ObservedDeliveryApproval{AttentionID: duty.AttentionID, DecisionID: decision.DecisionID, DecisionVersion: decision.Version, DecisionStatus: decision.Status}
		if err := q.QueryRowContext(ctx, `SELECT message_id,content_revision FROM issue_delivery_answers WHERE decision_id=?`, decision.DecisionID).Scan(&approval.SourceMessageID, &approval.SourceRevision); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		var answer struct {
			Option string `json:"option"`
		}
		if decision.AnswerJSON != "" {
			if err := json.Unmarshal([]byte(decision.AnswerJSON), &answer); err != nil {
				return nil, err
			}
		}
		approval.Option = answer.Option
		option, err := store.DeliveryOwnerOption(ctx, q, duty)
		if err != nil {
			return nil, err
		}
		approval.Current = option == "approve"
		if approval.SourceMessageID != "" {
			source, err := store.GetMessage(ctx, q, approval.SourceMessageID)
			if err != nil {
				return nil, err
			}
			associations, err := store.MessageAssociations(ctx, q, source.MessageID)
			if err != nil {
				return nil, err
			}
			approval.CurrentSourceRevision, approval.AnsweredAt = associations.ContentRevision, source.CreatedAt
			if err := q.QueryRowContext(ctx, `SELECT COALESCE(request_id,'') FROM conversation_messages WHERE message_id=?`, source.MessageID).Scan(&approval.SourceRequestID); err != nil {
				return nil, err
			}
		}
		v.Approval = approval
	}
	observed, err := store.GetDeliveryObservation(ctx, q, v.IssueID, v.Candidate.DeliveryID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		o := observed.Observation
		v.Provider = &protocol.ObservedDeliveryProvider{TargetContainsMerge: o.TargetContainsMerge, LineageErrorKind: o.LineageErrorKind, CI: o.CI, Mergeability: o.Mergeability, Generation: o.Generation, State: o.State, HeadSHA: o.HeadSHA, BaseSHA: o.BaseSHA, TargetSHA: o.TargetSHA, MergeSHA: o.MergeSHA, CheckedAt: o.CheckedAt, AttemptedAt: o.AttemptedAt, RefreshPending: o.RefreshPending, ErrorKind: o.ErrorKind}
	}
	v.SharedReview, err = observedDeliveryReview(ctx, q, issue, v.Candidate, v.SourceTaskID)
	if err != nil {
		return nil, err
	}
	primary, err := store.GetTaskIssueOwner(ctx, q, task)
	if err != nil {
		return nil, err
	}
	switch {
	case primary != issue.IssueID || frozen.AssignmentEpoch != issue.AssignmentEpoch || duty.AssignmentEpoch != issue.AssignmentEpoch:
		v.InvalidReason = "issue_responsibility_changed"
	case issue.AssignmentState != "active" || issue.State != protocol.IssueActive:
		v.InvalidReason = "issue_not_active"
	case duty.Revision != v.DutyRevision || duty.ExecutionTaskID != task || duty.Status != "executing":
		v.InvalidReason = "delivery_execution_not_current"
	case !reflect.DeepEqual(duty.Candidate, &v.Candidate) || !reflect.DeepEqual(duty.Authorization, v.Grant):
		v.InvalidReason = "frozen_authorization_changed"
	default:
		if err := store.DeliveryGrantValid(ctx, q, issue, duty); err != nil {
			var pe *protocol.Error
			if !errors.As(err, &pe) && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			v.InvalidReason = "authorization_not_current"
		}
	}
	if v.InvalidReason == "" && !slices.ContainsFunc(issue.Plan.Deliveries, func(d protocol.IssueDelivery) bool {
		return d.DeliveryID == v.Candidate.DeliveryID && d.URL == v.Candidate.URL
	}) {
		v.InvalidReason = "delivery_reference_changed"
	}
	if v.InvalidReason == "" {
		o := observed.Observation
		// A completed merge is a result within this execution's approved scope.
		// Retain the original candidate and expose the provider's merged state;
		// this does not authorize another merge or a different deployment source.
		advancedMergeOnly := !v.Candidate.Deploy && o.Observer == "server" && o.LineageErrorKind == "" && o.TargetSHA != ""
		merged := v.Candidate.ProviderState == "open" && o.State == "merged" && o.HeadSHA == v.Candidate.HeadSHA && o.BaseSHA == v.Candidate.BaseSHA && o.MergeSHA != "" && (o.TargetSHA == o.MergeSHA || advancedMergeOnly) && o.TargetContainsMerge
		if v.Provider == nil || o.ErrorKind != "" || o.RefreshPending || o.CheckedAt == 0 || (o.Observer == "server" && o.LineageErrorKind != "") {
			v.InvalidReason = "provider_observation_unavailable"
		} else if !merged && (o.State != v.Candidate.ProviderState || o.HeadSHA != v.Candidate.HeadSHA || o.BaseSHA != v.Candidate.BaseSHA || o.TargetSHA != v.Candidate.TargetSHA || (v.Candidate.ProviderState == "merged" && (o.MergeSHA != v.Candidate.MergeSHA || !o.TargetContainsMerge))) {
			v.InvalidReason = "provider_candidate_changed"
		}
	}
	if v.InvalidReason == "" && v.Grant != nil && policy != nil {
		if err := store.RequireDeliveryDutyReview(ctx, q, issue, duty); err != nil {
			var pe *protocol.Error
			if !errors.As(err, &pe) && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			v.InvalidReason = "shared_review_not_current"
		}
		if v.InvalidReason == "" && observed.Observation.State == "open" {
			if err := store.RequireDeliveryChecks(ctx, q, issue, v.Candidate); err != nil {
				var pe *protocol.Error
				if !errors.As(err, &pe) && !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
				v.InvalidReason = "delivery_checks_not_current"
			}
		}
	}
	v.Valid = v.InvalidReason == ""
	return v, nil
}

func observedDeliveryReview(ctx context.Context, q store.Querier, issue protocol.Issue, candidate protocol.IssueDeliveryCandidate, sourceTask string) (*protocol.ObservedDeliveryReview, error) {
	index := slices.IndexFunc(issue.Plan.Deliveries, func(ref protocol.IssueDelivery) bool {
		return ref.DeliveryID == candidate.DeliveryID && ref.URL == candidate.URL
	})
	if index < 0 {
		return nil, nil
	}
	review, err := store.SharedDeliveryReviewForTarget(ctx, q, issue.Plan.Deliveries[index])
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(review.Links, func(link protocol.SharedDeliveryReviewLink) bool {
		return link.IssueID == issue.IssueID && link.DeliveryID == candidate.DeliveryID && link.TaskID == sourceTask
	}) {
		return nil, nil
	}
	if err := store.SharedDeliveryReviewCurrent(ctx, q, &review); err != nil {
		return nil, err
	}
	if review.HeadSHA != candidate.HeadSHA || review.BaseSHA != candidate.BaseSHA {
		review.Valid, review.InvalidReason = false, "frozen candidate differs from reviewed candidate"
	}
	return observedSharedReview(review), nil
}

func observedSharedReview(review protocol.SharedDeliveryReview) *protocol.ObservedDeliveryReview {
	v := &protocol.ObservedDeliveryReview{Provider: review.Provider, Repository: review.Repository, Number: review.Number, SuggestedRounds: 2, RoundsUsed: len(review.Rounds), Source: "server.shared_delivery_reviews", ReviewID: review.ReviewID, Revision: review.Revision, Status: review.Status, Reviewer: review.Reviewer, HeadSHA: review.HeadSHA, BaseSHA: review.BaseSHA, RequirementsDigest: review.RequirementsDigest, AssessedRequirementsDigest: review.AssessedRequirementsDigest, Valid: review.Valid, InvalidReason: review.InvalidReason}
	if len(review.Rounds) > 0 {
		round := review.Rounds[len(review.Rounds)-1]
		v.Round = &protocol.ObservedDeliveryReviewRound{ReviewerRole: round.ReviewerRole, ReviewerProfile: round.ReviewerProfile, ExecutionRequirements: round.ExecutionRequirements, AcceptanceVerdict: round.AcceptanceVerdict, Number: round.Number, TaskID: round.TaskID, SpecVersion: round.SpecVersion, RunID: round.RunID, ArtifactID: round.ArtifactID, AcceptanceID: round.AcceptanceID, Reviewer: round.Reviewer, HeadSHA: round.HeadSHA, BaseSHA: round.BaseSHA, RequirementsDigest: round.Requirements}
		v.Round.SupplementTaskID, v.Round.SupplementSpecVersion = round.SupplementTaskID, round.SupplementSpecVersion
		v.Round.SupplementRunID, v.Round.SupplementArtifactID = round.SupplementRunID, round.SupplementArtifactID
		if len(round.Corrections) > 0 {
			c := round.Corrections[len(round.Corrections)-1]
			v.Round.Correction = &protocol.ObservedReviewCorrection{RequestID: c.RequestID, TaskID: c.TaskID, SpecVersion: c.SpecVersion, RunID: c.RunID, ArtifactID: c.ArtifactID, Profile: c.Profile, CreatedAt: c.CreatedAt}
		}
		if round.Result != nil {
			v.Round.Verdict, v.Round.RepositoryCheck = round.Result.Verdict, round.Result.RepositoryCheck
		}
	}
	return v
}
