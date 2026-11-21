package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ReviewIssueDelivery reuses actual reviewer tasks and their immutable results.
// It does not execute a model, synthesize acceptance or mutate a provider.
type ReviewIssueDelivery struct{}

func (ReviewIssueDelivery) Operation() string { return "issues.review_delivery" }
func (ReviewIssueDelivery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (ReviewIssueDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "an independent Issue target is required")
	}
	var p protocol.UpdateSharedDeliveryReviewPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	i, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if i.Revision != expected || i.AssignmentEpoch != p.ExpectedAssignmentEpoch || i.AssignmentState != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue responsibility changed")
	}
	if i.State != protocol.IssueActive && p.Action != "correct" || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "an active Issue or an immutable report correction and explicit review reason are required")
	}
	var ref protocol.IssueDelivery
	for _, d := range i.Plan.Deliveries {
		if d.DeliveryID == p.DeliveryID {
			ref = d
		}
	}
	if ref.DeliveryID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery_id must identify this Issue's registered PR")
	}
	r, err := store.SharedDeliveryReviewForTarget(ctx, tx.Q(), ref)
	if errors.Is(err, store.ErrNotFound) {
		if p.Action != "link" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "link the complete delivery before starting its shared review")
		}
		r = protocol.SharedDeliveryReview{ReviewID: x.IDs.New(protocol.Prefix("review")), Provider: ref.Provider, Repository: ref.Repository, Number: ref.Number, Status: "pending", Links: []protocol.SharedDeliveryReviewLink{}, Rounds: []protocol.SharedDeliveryReviewRound{}}
	} else if err != nil {
		return Outcome{}, err
	}
	affectedIssues := map[string]bool{i.IssueID: true}
	for _, link := range r.Links {
		affectedIssues[link.IssueID] = true
	}
	if p.Action == "link" || p.Action == "unlink" {
		if r.Status == "reviewing" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "settle the current frozen review before changing its scope")
		}
		if p.TaskID != "" {
			primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), p.TaskID)
			if errors.Is(err, store.ErrNotFound) {
				manager, managerErr := store.GetIssueManagerTask(ctx, tx.Q(), p.TaskID)
				if managerErr == nil {
					primary, err = manager.IssueID, nil
				}
			}
			if err != nil {
				return Outcome{}, err
			}
			if primary != i.IssueID {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "link only this Issue's source task")
			}
		}
		// The row must exist before its first foreign-keyed association.
		if r.Revision == 0 {
			raw, _ := json.Marshal(r)
			if _, err := tx.Exec(ctx, `INSERT INTO shared_delivery_reviews(review_id,provider,repository,number,revision,body_json) VALUES(?,?,?,?,0,?)`, r.ReviewID, r.Provider, r.Repository, r.Number, string(raw)); err != nil {
				return Outcome{}, err
			}
		}
		link := protocol.SharedDeliveryReviewLink{IssueID: i.IssueID, DeliveryID: p.DeliveryID, TaskID: p.TaskID}
		if p.Action == "link" {
			if !slices.Contains(r.Links, link) {
				r.Links = append(r.Links, link)
				if _, err := tx.Exec(ctx, `INSERT INTO shared_delivery_review_links(review_id,issue_id,delivery_id,task_id) VALUES(?,?,?,?)`, r.ReviewID, i.IssueID, p.DeliveryID, p.TaskID); err != nil {
					return Outcome{}, err
				}
			}
		} else {
			r.Links = slices.DeleteFunc(r.Links, func(l protocol.SharedDeliveryReviewLink) bool { return l == link })
			if _, err := tx.Exec(ctx, `DELETE FROM shared_delivery_review_links WHERE review_id=? AND issue_id=? AND delivery_id=? AND task_id=?`, r.ReviewID, i.IssueID, p.DeliveryID, p.TaskID); err != nil {
				return Outcome{}, err
			}
		}
	} else {
		if !slices.ContainsFunc(r.Links, func(l protocol.SharedDeliveryReviewLink) bool {
			return l.IssueID == i.IssueID && l.DeliveryID == p.DeliveryID
		}) {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "this Issue has not joined the shared delivery review")
		}
		switch p.Action {
		case "start", "recheck":
			if p.Action == "recheck" && r.Status == "passed" {
				current := r
				changed, err := store.SharedDeliveryReviewChanged(ctx, tx.Q(), &current)
				if err != nil {
					return Outcome{}, err
				}
				// An applicable assessment can be superseded by a real review.
				// Keep the existing deduplication for a directly applicable round.
				if current.Valid && (current.Assessment == nil ||
					(current.CoversCandidate(r.HeadSHA, r.BaseSHA) && current.CurrentCandidate.RequirementsDigest == r.RequirementsDigest)) {
					return Outcome{Result: current}, nil
				}
				if !current.Valid && !changed {
					return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "await the current bounded server observation; temporary provider uncertainty retains the actual passed review and its original budget")
				}
			}
			if r.Status == "reviewing" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the delivery already has an active reviewer")
			}
			eligible := r.Status == "passed" || r.Status == "waiting" || (r.Status == "changes_requested" || r.Status == "escalated") && reviewFindingsResolved(r)
			if p.Action == "recheck" && (len(r.Rounds) == 0 || !eligible) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "one consolidated finding disposition is required before the original reviewer's recheck")
			}
			if p.Action == "start" && len(r.Rounds) > 0 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "retain the original reviewer and use a focused recheck")
			}
			o, err := store.GetDeliveryObservation(ctx, tx.Q(), i.IssueID, ref.DeliveryID)
			if err != nil {
				return Outcome{}, err
			}
			if o.Observation.ErrorKind != "" || o.Observation.RefreshPending || !validDeliverySHA(o.Observation.HeadSHA) || !validDeliverySHA(o.Observation.BaseSHA) {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "observe the exact review candidate before starting")
			}
			digest, err := store.SharedReviewRequirements(ctx, tx.Q(), r)
			if err != nil {
				return Outcome{}, err
			}
			if p.ExpectedHeadSHA != "" || p.ExpectedBaseSHA != "" || p.ExpectedRequirementsDigest != "" {
				if p.ExpectedHeadSHA != o.Observation.HeadSHA || p.ExpectedBaseSHA != o.Observation.BaseSHA || p.ExpectedRequirementsDigest != digest {
					return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "review candidate or requirements changed; read the current shared review before binding its actual reviewer")
				}
			}
			if p.ReviewerRole != "" && p.ReviewerRole != protocol.RoleAccept {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "reviewer_role must be accept or omitted for a report task")
			}
			var round protocol.SharedDeliveryReviewRound
			if p.ReviewerRole == protocol.RoleAccept {
				binding := r
				binding.HeadSHA, binding.BaseSHA, binding.RequirementsDigest = o.Observation.HeadSHA, o.Observation.BaseSHA, digest
				round, err = bindSharedAcceptanceReview(ctx, tx.Q(), binding, p.ReviewerTaskID, p.AcceptanceID)
			} else {
				round, err = bindSharedReviewTask(ctx, tx.Q(), r, p.ReviewerTaskID, p.AcceptanceID)
			}
			if err != nil {
				return Outcome{}, err
			}
			if round.ArtifactID == "" && round.TaskID != "" {
				target := independentReviewPRTarget(ref, o.Observation.HeadSHA, o.Observation.BaseSHA)
				if allowed, err := canStartIndependentReviewTarget(ctx, tx.Q(), target, round.TaskID); err != nil {
					return Outcome{}, err
				} else if !allowed {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "resolve the original independent review Task for this candidate")
				}
			}
			// A completed report already declares its actual candidate. Explicit
			// acceptance imports retain their original evidence/supplement path.
			if round.ArtifactID != "" && round.AcceptanceID == "" && round.ReviewerRole != protocol.RoleAccept {
				binding := r
				binding.HeadSHA, binding.BaseSHA, binding.RequirementsDigest = o.Observation.HeadSHA, o.Observation.BaseSHA, digest
				frozen := round
				frozen.HeadSHA, frozen.BaseSHA, frozen.Requirements = binding.HeadSHA, binding.BaseSHA, digest
				if _, _, _, err := store.ReadSharedReviewResult(ctx, tx.Q(), binding, frozen); err != nil {
					return Outcome{}, err
				}
			}
			waitingEvidence := false
			if len(r.Rounds) > 0 {
				last := r.Rounds[len(r.Rounds)-1]
				// Coordination status changes on an external budget grant.
				// Eligibility belongs to the last immutable waiting verdict.
				waitingEvidence = last.ReviewerRole == protocol.RoleAccept && last.TaskID == round.TaskID && last.ArtifactID == round.ArtifactID && (last.AcceptanceVerdict == protocol.VerdictBlocked || last.AcceptanceVerdict == protocol.VerdictInconclusive)
			}
			for _, previous := range r.Rounds {
				previousReport := previous
				if previous.SupplementTaskID != "" && previous.SupplementTaskID == round.TaskID {
					previousReport = protocol.SharedReviewInputRound(previous)
				}
				sameTask := previousReport.TaskID == round.TaskID
				accept := round.ReviewerRole == protocol.RoleAccept
				if sameTask && !accept && previousReport.RunID != "" &&
					(previousReport.SpecVersion == 0 || previousReport.ImplementGeneration == 0) {
					oldRun, err := store.GetRun(ctx, tx.Q(), previousReport.RunID)
					if err != nil {
						return Outcome{}, err
					}
					oldAction, err := store.GetAction(ctx, tx.Q(), oldRun.ActionID)
					if err != nil {
						return Outcome{}, err
					}
					if previousReport.SpecVersion == 0 {
						previousReport.SpecVersion = oldAction.TargetSpecVersion
					}
					if previousReport.ImplementGeneration == 0 {
						previousReport.ImplementGeneration = oldAction.Generation
					}
				}
				// Implement generations restart when a new Task spec becomes active.
				sameSpec := previousReport.SpecVersion == 0 || previousReport.SpecVersion == round.SpecVersion
				sameGeneration := previousReport.ImplementGeneration == 0 || previousReport.ImplementGeneration == round.ImplementGeneration
				sameReport := sameTask && !accept && sameSpec && sameGeneration
				sameRun := round.RunID != "" && (previous.RunID == round.RunID || previous.SupplementRunID == round.RunID)
				if sameReport || sameTask && accept && previous.ArtifactID == round.ArtifactID && !waitingEvidence || sameRun || round.AcceptanceID != "" && previous.AcceptanceID == round.AcceptanceID {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "a focused review requires a new real run/result on the sustained Task; earlier runs or acceptances cannot be recounted")
				}
			}
			if r.Reviewer != "" && r.Reviewer != round.Reviewer {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "the focused recheck belongs to the original formal reviewer")
			}
			r.HeadSHA, r.BaseSHA, r.RequirementsDigest, r.Reviewer, r.Status = o.Observation.HeadSHA, o.Observation.BaseSHA, digest, round.Reviewer, "reviewing"
			r.AssessedRequirementsDigest, r.AssessmentReason, r.Assessment = "", "", nil
			round.Number, round.HeadSHA, round.BaseSHA, round.Requirements = len(r.Rounds)+1, r.HeadSHA, r.BaseSHA, digest
			r.Rounds = append(r.Rounds, round)
			// Earlier dispositions remain in immutable review history; they do
			// not settle recurring finding IDs in the next actual review.
			r.Dispositions = nil
		case "record":
			if r.Status != "reviewing" || len(r.Rounds) == 0 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "there is no active frozen shared review")
			}
			last := &r.Rounds[len(r.Rounds)-1]
			previousAcceptance, previousVerdict := last.AcceptanceID, last.AcceptanceVerdict
			inputRound := protocol.SharedReviewInputRound(*last)
			var result *protocol.SharedReviewResult
			var run, artifact string
			if inputRound.ReviewerRole == protocol.RoleAccept {
				result, run, artifact, err = store.ReadSharedAcceptanceResult(ctx, tx.Q(), r, &inputRound)
				last.AcceptanceID, last.AcceptanceVerdict = inputRound.AcceptanceID, inputRound.AcceptanceVerdict
			} else {
				result, run, artifact, err = store.ReadSharedReviewResult(ctx, tx.Q(), r, inputRound)
			}
			if errors.Is(err, store.ErrAcceptanceReviewEvidence) {
				if previousAcceptance == inputRound.AcceptanceID && previousVerdict != "" && previousVerdict == inputRound.AcceptanceVerdict {
					return Outcome{Result: r}, nil
				}
				// Preserve the real acceptance without promoting a narrow or
				// malformed verdict into whole-PR approval. The existing
				// original-reviewer supplement can close this same round.
				last.RunID, last.ArtifactID = run, artifact
				break
			}
			if err != nil {
				return Outcome{}, err
			}
			last.Result = result
			if last.SupplementTaskID != "" {
				last.SupplementRunID, last.SupplementArtifactID = run, artifact
			} else {
				last.RunID, last.ArtifactID = run, artifact
			}
			r.Status = "changes_requested"
			if result.Verdict == "passed" {
				r.Status = "passed"
			}
			if result.Verdict == "blocked" || result.Verdict == "inconclusive" {
				r.Status = "waiting"
			}

		case "wait":
			if p.ExpectedReviewRevision == nil || *p.ExpectedReviewRevision != r.Revision {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "wait requires the current expected_review_revision")
			}
			if r.Status != "reviewing" || len(r.Rounds) == 0 || r.Rounds[len(r.Rounds)-1].Result != nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "wait requires an unresolved frozen review without a quality conclusion")
			}
			last := protocol.SharedReviewInputRound(r.Rounds[len(r.Rounds)-1])
			if err := stoppedTaskRecoveryReady(ctx, tx.Q(), last.TaskID, false); err != nil {
				return Outcome{}, err
			}
			if last.ReviewerRole == protocol.RoleAccept {
				if _, _, _, err := store.ReadSharedAcceptanceResult(ctx, tx.Q(), r, &last); err == nil {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "record the actual acceptance instead of replacing its conclusion with waiting")
				} else if protocol.CodeOf(err) != protocol.CodeInvalidTransition && !errors.Is(err, store.ErrAcceptanceReviewEvidence) {
					return Outcome{}, err
				}
			} else if _, _, _, err := store.ReadSharedReviewResult(ctx, tx.Q(), r, last); err == nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "record the actual review report instead of replacing its conclusion with waiting")
			} else if protocol.CodeOf(err) != protocol.CodeInvalidTransition && protocol.CodeOf(err) != protocol.CodeInvalidRequest {
				return Outcome{}, err
			}
			r.Status = "waiting"
		case "authorize_recheck":
			if x.Principal.Kind != protocol.PrincipalOwner || x.coordinationExecution != nil {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only an independent authenticated external user may authorize an additional review round")
			}
			if p.ExpectedReviewRevision == nil || *p.ExpectedReviewRevision < 1 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "expected_review_revision is required for additional review authority")
			}
			if *p.ExpectedReviewRevision != r.Revision {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "shared review changed before the additional round was authorized")
			}
			if r.Status != "escalated" || len(r.Rounds) != sharedReviewRoundLimit(r) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "authorize exactly one further round only after the current review budget is exhausted")
			}
			r.BudgetAuthorizations = append(r.BudgetAuthorizations, protocol.SharedReviewBudgetAuthorization{RequestID: x.Env.RequestID, Round: len(r.Rounds) + 1, Reason: p.Reason})
			r.Status = "changes_requested"
		case "correct":
			if p.ExpectedReviewRevision == nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "expected_review_revision is required for a report correction")
			}
			if *p.ExpectedReviewRevision != r.Revision {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "the original shared review changed before its report correction")
			}
			if len(r.Rounds) == 0 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "there is no original review report to correct")
			}
			last := &r.Rounds[len(r.Rounds)-1]
			original, err := store.GetTask(ctx, tx.Q(), last.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			if !original.Lifecycle.Terminal() {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "retain the original review until its actual report settles")
			}
			if last.AcceptanceID == "" {
				originalArtifact := last.ArtifactID
				if originalArtifact == "" {
					originalArtifact = original.CurrentArtifactID
				}
				if originalArtifact == "" {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "there is no immutable original report to correct")
				}
				artifact, err := store.GetArtifact(ctx, tx.Q(), originalArtifact)
				if err != nil {
					return Outcome{}, err
				}
				var originalReport protocol.ArtifactPayload
				if err := json.Unmarshal([]byte(artifact.RefJSON), &originalReport); err != nil {
					return Outcome{}, err
				}
				pin, err := store.GetRunExecution(ctx, tx.Q(), artifact.RunID)
				if err != nil {
					return Outcome{}, err
				}
				originalRun, err := store.GetRun(ctx, tx.Q(), artifact.RunID)
				if err != nil {
					return Outcome{}, err
				}
				action, err := store.GetAction(ctx, tx.Q(), originalRun.ActionID)
				if err != nil {
					return Outcome{}, err
				}
				if artifact.TaskID != last.TaskID || action.TargetSpecVersion != last.SpecVersion || originalReport.Report == nil || pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID != r.Reviewer {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "retain the original reviewer's actual immutable report")
				}
				last.RunID, last.ArtifactID = artifact.RunID, artifact.ArtifactID
			}
			correction, err := bindSharedReviewTask(ctx, tx.Q(), r, p.ReviewerTaskID, "", last.ExecutionRequirements)
			if err != nil {
				return Outcome{}, err
			}
			if correction.Reviewer != r.Reviewer {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "a report correction must come from the original reviewer")
			}
			correction.HeadSHA, correction.BaseSHA, correction.Requirements = last.HeadSHA, last.BaseSHA, last.Requirements
			result, run, artifact, err := store.ReadSharedReviewResult(ctx, tx.Q(), r, correction)
			if err != nil {
				return Outcome{}, err
			}
			if run == last.RunID || artifact == last.ArtifactID || slices.ContainsFunc(last.Corrections, func(c protocol.SharedReviewCorrection) bool { return c.RunID == run || c.ArtifactID == artifact }) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the report is already recorded; preserve its original receipt")
			}
			pin, err := store.GetRunExecution(ctx, tx.Q(), run)
			if err != nil {
				return Outcome{}, err
			}
			last.Corrections = append(last.Corrections, protocol.SharedReviewCorrection{RequestID: x.Env.RequestID, TaskID: correction.TaskID, SpecVersion: correction.SpecVersion, RunID: run, ArtifactID: artifact, Profile: pin.Profile, Result: *result, Reason: p.Reason, CreatedAt: x.Now.UnixMilli()})
			r.AssessedRequirementsDigest, r.AssessmentReason, r.Assessment = "", "", nil
			last.Result = result
			r.Status = "changes_requested"
			if result.Verdict == "passed" {
				r.Status = "passed"
			}
			if result.Verdict == "blocked" || result.Verdict == "inconclusive" {
				r.Status = "waiting"
			}
		case "supplement":
			if r.Status != "reviewing" || len(r.Rounds) == 0 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only an unresolved original acceptance import can receive focused evidence")
			}
			last := &r.Rounds[len(r.Rounds)-1]
			if last.AcceptanceID == "" || last.Result != nil || last.SupplementTaskID != "" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "preserve the original result and review budget; one original-reviewer evidence supplement is allowed")
			}
			bound, err := bindSharedReviewTask(ctx, tx.Q(), r, p.ReviewerTaskID, "", last.ExecutionRequirements)
			if err != nil {
				return Outcome{}, err
			}
			if bound.Reviewer != r.Reviewer {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only the original formal reviewer may supplement its evidence")
			}
			if bound.TaskID == last.TaskID && bound.ImplementGeneration <= last.ImplementGeneration {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "continue the original reviewer Task to produce new supplement evidence")
			}
			last.SupplementTaskID, last.SupplementSpecVersion = bound.TaskID, bound.SpecVersion
			last.SupplementGeneration = bound.ImplementGeneration
		case "assess":
			if err := assessSharedReview(ctx, x, tx.Q(), i, p, &r); err != nil {
				return Outcome{}, err
			}
		case "resolve":
			if r.Status != "changes_requested" && r.Status != "escalated" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only pending review findings accept dispositions")
			}
			if err := validateReviewDispositions(r, p.Dispositions); err != nil {
				return Outcome{}, err
			}
			r.Dispositions = slices.Clone(p.Dispositions)
		default:
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review action must be link, unlink, start, record, wait, correct, supplement, resolve, assess, recheck or authorize_recheck")
		}
	}
	if len(r.Rounds) == 0 {
		// Expose the exact input before the owner creates the reviewer task, so
		// its frozen instructions can name the real review and requirements.
		r.RequirementsDigest, err = store.SharedReviewRequirements(ctx, tx.Q(), r)
		if err != nil {
			return Outcome{}, err
		}
		o, err := store.GetDeliveryObservation(ctx, tx.Q(), i.IssueID, ref.DeliveryID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Outcome{}, err
		}
		if err == nil && o.Observation.ErrorKind == "" && !o.Observation.RefreshPending {
			r.HeadSHA, r.BaseSHA = o.Observation.HeadSHA, o.Observation.BaseSHA
		}
	}
	r.Revision++
	r.Reason = p.Reason
	if err := tx.PutSharedDeliveryReview(ctx, r, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	if err := store.SharedDeliveryReviewCurrent(ctx, tx.Q(), &r); err != nil {
		return Outcome{}, err
	}
	for _, link := range r.Links {
		affectedIssues[link.IssueID] = true
	}
	ids := make([]string, 0, len(affectedIssues))
	for id := range affectedIssues {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	events := make([]Event, 0, len(ids))
	for _, id := range ids {
		events = append(events, Event{ObjectType: "issue", ObjectID: id, Kind: "issue.delivery_review_changed", Body: map[string]any{"review_id": r.ReviewID, "review_revision": r.Revision, "status": r.Status}})
	}
	return Outcome{Result: r, Notify: true, Events: events}, nil
}

func assessSharedReview(ctx context.Context, x *Exec, q store.Querier, issue protocol.Issue, p protocol.UpdateSharedDeliveryReviewPayload, r *protocol.SharedDeliveryReview) error {
	if r.Status != "passed" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "only a passed review can be confirmed applicable")
	}
	if err := store.SharedDeliveryReviewCurrent(ctx, q, r); err != nil {
		return err
	}
	candidate := r.CurrentCandidate
	if candidate == nil {
		return protocol.Errorf(protocol.CodeStaleRevision, "observe the current review candidate before assessing applicability")
	}
	if p.ExpectedCandidateRevision == nil {
		// The original contract accepted optional fields used by other
		// actions. It only assesses requirements on the already reviewed pair.
		if candidate.HeadSHA != r.HeadSHA || candidate.BaseSHA != r.BaseSHA {
			return protocol.Errorf(protocol.CodeStaleRevision, "read the current candidate and bind the applicability assessment or arrange a focused recheck")
		}
		r.Assessment = nil
		r.AssessedRequirementsDigest, r.AssessmentReason = candidate.RequirementsDigest, p.Reason
		return nil
	}
	// Hosted authorization already checked the current named agent and epoch.
	// A direct call must come from the human assigned to this Issue.
	if x.coordinationExecution == nil && issue.Assignee != (protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: x.Principal.Subject}) {
		return protocol.Errorf(protocol.CodeForbidden, "the current named Issue owner must assess a changed review candidate")
	}
	if p.ExpectedReviewRevision == nil || !validDeliverySHA(p.ExpectedHeadSHA) ||
		!validDeliverySHA(p.ExpectedBaseSHA) || p.ExpectedRequirementsDigest == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "assessment requires the review revision, candidate revision, head, base and requirements digest")
	}
	if *p.ExpectedReviewRevision != r.Revision || *p.ExpectedCandidateRevision != candidate.Revision ||
		p.ExpectedHeadSHA != candidate.HeadSHA || p.ExpectedBaseSHA != candidate.BaseSHA ||
		p.ExpectedRequirementsDigest != candidate.RequirementsDigest {
		return protocol.Errorf(protocol.CodeStaleRevision, "review, provider candidate or requirements changed before the applicability assessment")
	}
	if len(r.Rounds) == 0 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the original passed review round is missing")
	}
	round := r.Rounds[len(r.Rounds)-1]
	if round.Result == nil || round.Result.Verdict != "passed" || round.RunID == "" || round.ArtifactID == "" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "retain the actual passed review result and its execution evidence")
	}
	r.AssessedRequirementsDigest, r.AssessmentReason = "", ""
	r.Assessment = &protocol.SharedReviewAssessment{
		Candidate: *candidate, Round: round.Number, ReviewRevision: r.Revision,
		IssueID: issue.IssueID, Assessor: issue.Assignee, AssignmentEpoch: issue.AssignmentEpoch, Reason: p.Reason,
	}
	if err := store.SharedDeliveryReviewCurrent(ctx, q, r); err != nil {
		return err
	}
	if !r.Valid {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the original review does not satisfy the current delivery requirements")
	}
	return nil
}

func sharedReviewRoundLimit(r protocol.SharedDeliveryReview) int {
	return 2 + len(r.BudgetAuthorizations)
}

func bindSharedReviewTask(ctx context.Context, q store.Querier, r protocol.SharedDeliveryReview, task, acceptance string, frozenRequirements ...*protocol.SharedReviewExecutionRequirements) (protocol.SharedDeliveryReviewRound, error) {
	out := protocol.SharedDeliveryReviewRound{TaskID: task, AcceptanceID: acceptance}
	t, spec, err := loadTask(ctx, q, task)
	if err != nil {
		return out, err
	}
	out.SpecVersion = t.SpecVersion
	out.ImplementGeneration = t.ImplementGeneration
	if acceptance != "" {
		rows, err := store.ListAcceptances(ctx, q, task, t.CurrentArtifactID)
		if err != nil {
			return out, err
		}
		for _, a := range rows {
			if a.AcceptanceID == acceptance && a.SpecVersion == t.SpecVersion {
				out.RunID = a.RunID
				out.ArtifactID = a.ArtifactID
			}
		}
		if out.RunID == "" {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "acceptance must be the task's actual current artifact verdict")
		}
		pin, err := store.GetRunExecution(ctx, q, out.RunID)
		if err != nil {
			return out, err
		}
		out.Reviewer = pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
	} else {
		out, err = bindSharedReportExecution(ctx, q, t, spec)
		if err != nil {
			return out, err
		}
	}
	requirements, err := store.SharedReviewExecutionRequirements(ctx, q, r)
	if err != nil {
		return out, err
	}
	if len(frozenRequirements) > 0 && frozenRequirements[0] != nil {
		requirements = frozenRequirements[0]
	}
	if err := validateSharedReviewTaskProfile(ctx, q, requirements, t, spec, out.RunID); err != nil {
		return out, err
	}
	out.ExecutionRequirements = requirements
	for _, link := range r.Links {
		if link.TaskID != "" && acceptance == "" {
			_, sourceSpec, err := loadTask(ctx, q, link.TaskID)
			if err != nil {
				return out, err
			}
			if sourceSpec.AgentSelection != nil && sourceSpec.AgentSelection.Acceptance.CategoryID != "" {
				required := sourceSpec.AgentSelection.Acceptance
				actual := spec.AgentSelection.Implementation
				if actual.CategoryID != required.CategoryID || required.AgentID != "" && required.AgentID != out.Reviewer {
					return out, protocol.Errorf(protocol.CodeForbidden, "shared reviewer must retain the linked delivery's formal category, identity and profile")
				}
			}
		}
		if link.TaskID == "" {
			continue
		}
		rows, err := store.FollowupIDs(ctx, q, `SELECT run_id FROM runs WHERE task_id=? AND role IN ('implement','issue_manager')`, link.TaskID)
		if err != nil {
			return out, err
		}
		for _, run := range rows {
			pin, err := store.GetRunExecution(ctx, q, run)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			if out.Reviewer == pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID {
				return out, protocol.Errorf(protocol.CodeForbidden, "the formal reviewer must be independent of the linked implementation")
			}
		}
	}
	// Running report work may yield and resume on a successor Run within the
	// same execution generation. Freeze its selected identity/profile now;
	// freeze the exact producing Run when the immutable report is available.
	if acceptance == "" && out.ArtifactID == "" {
		out.RunID = ""
	}
	return out, nil
}

// A category expresses the work; only an actual Run pin establishes its reviewer.
// Keep named prebinding for existing callers that start before scheduling.
func bindSharedReportExecution(ctx context.Context, q store.Querier, task store.TaskRow, spec domain.Spec) (protocol.SharedDeliveryReviewRound, error) {
	out := protocol.SharedDeliveryReviewRound{TaskID: task.TaskID, SpecVersion: task.SpecVersion, ImplementGeneration: task.ImplementGeneration}
	if spec.AgentSelection == nil || !spec.RequiresDelivery("report") {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "reviewer must submit an immutable report artifact")
	}
	out.Reviewer = spec.AgentSelection.Implementation.AgentID
	if task.Lifecycle == protocol.TaskCompleted && task.CurrentArtifactID != "" {
		artifact, err := store.GetArtifact(ctx, q, task.CurrentArtifactID)
		if err != nil {
			return out, err
		}
		if artifact.TaskID != task.TaskID {
			return out, protocol.Errorf(protocol.CodeInvalidTransition, "the review report must belong to its original Task")
		}
		out.RunID, out.ArtifactID = artifact.RunID, artifact.ArtifactID
	} else {
		ids, err := store.FollowupIDs(ctx, q, `SELECT r.run_id FROM runs r JOIN plan_actions a USING(action_id) WHERE r.task_id=? AND a.task_id=r.task_id AND r.role='implement' AND a.target_spec_version=? AND a.generation=? ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, task.TaskID, task.SpecVersion, task.ImplementGeneration)
		if err != nil {
			return out, err
		}
		if len(ids) > 0 {
			out.RunID = ids[0]
		}
	}
	if out.RunID == "" {
		if out.Reviewer == "" {
			return out, protocol.Errorf(protocol.CodeInvalidTransition, "the category reviewer has no actual Run for the current Task execution; wait for selection and retain the original Task")
		}
		return out, nil
	}
	run, err := store.GetRun(ctx, q, out.RunID)
	if err != nil {
		return out, err
	}
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return out, err
	}
	if run.TaskID != task.TaskID || run.Role != protocol.RoleImplement || action.TaskID != task.TaskID || action.TargetSpecVersion != task.SpecVersion || action.Generation != task.ImplementGeneration {
		return out, protocol.Errorf(protocol.CodeInvalidTransition, "the reviewer Run must match the current Task, spec and generation")
	}
	pin, err := store.GetRunExecution(ctx, q, out.RunID)
	if err != nil {
		return out, err
	}
	actual := pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
	if out.Reviewer != "" && out.Reviewer != actual {
		return out, protocol.Errorf(protocol.CodeForbidden, "the actual reviewer must match the Task's explicit identity")
	}
	out.Reviewer = actual
	out.ReviewerProfile = &pin.Profile
	return out, nil
}

func validateReviewDispositions(r protocol.SharedDeliveryReview, ds []protocol.SharedReviewDisposition) error {
	findings := r.Rounds[len(r.Rounds)-1].Result.Findings
	seen := map[string]bool{}
	for _, d := range ds {
		if seen[d.FindingID] || !slices.ContainsFunc(findings, func(f protocol.SharedReviewFinding) bool { return f.ID == d.FindingID }) || (d.Action != "fixed" && d.Action != "not_adopted") || strings.TrimSpace(d.Reason) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "each finding needs one fixed/not_adopted disposition and reason")
		}
		seen[d.FindingID] = true
	}
	if len(seen) != len(findings) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "resolve every finding before the focused recheck")
	}
	return nil
}
func reviewFindingsResolved(r protocol.SharedDeliveryReview) bool {
	return validateReviewDispositions(r, r.Dispositions) == nil
}
