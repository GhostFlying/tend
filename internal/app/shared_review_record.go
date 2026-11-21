package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// recordCompletedSharedReport registers the original report in one transaction.
// The Task owns execution continuity. Prebinding a generation is optional for
// report Tasks whose result already names the complete observed candidate.
func recordCompletedSharedReport(ctx context.Context, q store.Querier, issue protocol.Issue, p protocol.UpdateSharedDeliveryReviewPayload, r *protocol.SharedDeliveryReview) error {
	if p.ReviewerRole != "" && p.ReviewerRole != protocol.RoleImplement || p.AcceptanceID != "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "explicit report registration requires a completed report Task")
	}
	if p.ExpectedReviewRevision == nil || p.ExpectedCandidateRevision == nil || p.ExpectedHeadSHA == "" || p.ExpectedBaseSHA == "" || p.ExpectedRequirementsDigest == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "explicit report registration requires review revision and the complete observed candidate")
	}
	if *p.ExpectedReviewRevision != r.Revision {
		return protocol.Errorf(protocol.CodeStaleRevision, "the shared review changed before report registration")
	}
	changed, err := store.SharedDeliveryReviewChanged(ctx, q, r)
	if err != nil {
		return err
	}
	candidate := r.CurrentCandidate
	if candidate == nil || !validDeliverySHA(candidate.HeadSHA) || !validDeliverySHA(candidate.BaseSHA) ||
		candidate.Revision != *p.ExpectedCandidateRevision || candidate.HeadSHA != p.ExpectedHeadSHA ||
		candidate.BaseSHA != p.ExpectedBaseSHA || candidate.RequirementsDigest != p.ExpectedRequirementsDigest {
		return protocol.Errorf(protocol.CodeStaleRevision, "the provider candidate or review requirements changed before report registration")
	}
	if r.Status == "passed" && !changed && (r.Assessment == nil || r.HeadSHA == candidate.HeadSHA && r.BaseSHA == candidate.BaseSHA && r.RequirementsDigest == candidate.RequirementsDigest) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the candidate already has an applicable direct review")
	}
	if (r.Status == "changes_requested" || r.Status == "escalated") && !reviewFindingsResolved(*r) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "resolve the original findings before recording a focused recheck")
	}
	primary, err := store.GetTaskIssueOwner(ctx, q, p.ReviewerTaskID)
	if err != nil {
		return err
	}
	if primary != issue.IssueID {
		return protocol.Errorf(protocol.CodeForbidden, "the report Task must belong to this Issue")
	}
	task, spec, err := loadTask(ctx, q, p.ReviewerTaskID)
	if err != nil {
		return err
	}
	if spec.ReviewDeliveryID != p.DeliveryID || task.Lifecycle != protocol.TaskCompleted || task.CurrentArtifactID == "" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "await a completed report from a Task scoped to this registered PR")
	}
	if !slices.ContainsFunc(r.Links, func(link protocol.SharedDeliveryReviewLink) bool { return link.TaskID != "" }) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "link the actual implementation Task or Issue manager execution before registering a report")
	}
	round, err := bindSharedReviewTask(ctx, q, *r, task.TaskID, "")
	if err != nil {
		return err
	}
	if r.Reviewer != "" && round.Reviewer != r.Reviewer {
		return protocol.Errorf(protocol.CodeForbidden, "the report must come from the original independent reviewer")
	}
	round.HeadSHA, round.BaseSHA, round.Requirements = candidate.HeadSHA, candidate.BaseSHA, candidate.RequirementsDigest
	result, run, artifact, err := store.ReadSharedReviewResult(ctx, q, *r, round)
	if err != nil {
		return err
	}
	if err := stoppedTaskRecoveryReady(ctx, q, task.TaskID); err != nil {
		return err
	}

	// An old unresolved binding can be superseded only after checking its
	// original execution and available evidence. Keep real verdicts on the
	// original round, even if a newer report has already completed.
	if n := len(r.Rounds); n > 0 && r.Rounds[n-1].Result == nil {
		last := &r.Rounds[n-1]
		if last.ReviewerRole == protocol.RoleAccept || last.AcceptanceID != "" || last.SupplementTaskID != "" || last.TaskID != task.TaskID {
			return protocol.Errorf(protocol.CodeInvalidTransition, "settle the original review evidence through its existing Task and role")
		}
		_, priorRun, priorArtifact, err := store.ReadSharedReviewResult(ctx, q, *r, *last)
		switch {
		case err == nil && priorRun == run && priorArtifact == artifact:
			last.Result, last.RunID, last.ArtifactID, last.ReviewerProfile = result, run, artifact, round.ReviewerProfile
			r.Status = sharedReviewResultStatus(result.Verdict)
			return nil
		case err == nil:
			return protocol.Errorf(protocol.CodeInvalidTransition, "record the original report before its successor")
		case protocol.CodeOf(err) == protocol.CodeInvalidTransition || protocol.CodeOf(err) == protocol.CodeInvalidRequest:
		case errors.Is(err, store.ErrNotFound):
			return protocol.Errorf(protocol.CodeUnknownOutcome, "the original review evidence could not be reconciled")
		default:
			return err
		}
	}
	for _, previous := range r.Rounds {
		if previous.RunID == run || previous.SupplementRunID == run || previous.ArtifactID == artifact || previous.SupplementArtifactID == artifact {
			return protocol.Errorf(protocol.CodeInvalidTransition, "the actual report is already part of this shared review")
		}
		for _, correction := range previous.Corrections {
			if correction.RunID == run || correction.ArtifactID == artifact {
				return protocol.Errorf(protocol.CodeInvalidTransition, "the actual correction is already part of this shared review")
			}
		}
	}
	round.Number, round.Result = len(r.Rounds)+1, result
	r.Rounds = append(r.Rounds, round)
	r.HeadSHA, r.BaseSHA, r.RequirementsDigest, r.Reviewer = round.HeadSHA, round.BaseSHA, round.Requirements, round.Reviewer
	r.Status = sharedReviewResultStatus(result.Verdict)
	r.AssessedRequirementsDigest, r.AssessmentReason, r.Assessment = "", "", nil
	r.Dispositions = nil
	return nil
}

func sharedReviewResultStatus(verdict string) string {
	switch verdict {
	case "passed":
		return "passed"
	case "blocked", "inconclusive":
		return "waiting"
	default:
		return "changes_requested"
	}
}
