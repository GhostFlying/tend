package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

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
				if current.Valid {
					return Outcome{Result: current}, nil
				}
				if !changed {
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
			o, err := store.GetCurrentDeliveryObservation(ctx, tx.Q(), i.IssueID, ref)
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
			waitingEvidence := false
			if len(r.Rounds) > 0 {
				last := r.Rounds[len(r.Rounds)-1]
				// Coordination status changes on an external budget grant.
				// Eligibility belongs to the last immutable waiting verdict.
				waitingEvidence = last.ReviewerRole == protocol.RoleAccept && last.TaskID == round.TaskID && last.ArtifactID == round.ArtifactID && (last.AcceptanceVerdict == protocol.VerdictBlocked || last.AcceptanceVerdict == protocol.VerdictInconclusive)
			}
			for _, previous := range r.Rounds {
				sameTask := previous.TaskID == round.TaskID || previous.SupplementTaskID != "" && previous.SupplementTaskID == round.TaskID
				accept := round.ReviewerRole == protocol.RoleAccept
				if sameTask && (!accept || previous.ArtifactID == round.ArtifactID && !waitingEvidence) || round.RunID != "" && previous.RunID == round.RunID || round.AcceptanceID != "" && previous.AcceptanceID == round.AcceptanceID {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "a focused acceptance review requires a new real artifact/run; earlier tasks, runs or acceptances cannot be recounted")
				}
			}
			if r.Reviewer != "" && r.Reviewer != round.Reviewer {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "the focused recheck belongs to the original formal reviewer")
			}
			r.HeadSHA, r.BaseSHA, r.RequirementsDigest, r.Reviewer, r.Status = o.Observation.HeadSHA, o.Observation.BaseSHA, digest, round.Reviewer, "reviewing"
			r.AssessedRequirementsDigest, r.AssessmentReason = "", ""
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
			inputRound := *last
			if last.SupplementTaskID != "" {
				inputRound.TaskID = last.SupplementTaskID
				inputRound.SpecVersion = last.SupplementSpecVersion
				inputRound.AcceptanceID = ""
				inputRound.ReviewerRole = ""
			}
			var result *protocol.SharedReviewResult
			var run, artifact string
			if inputRound.ReviewerRole == protocol.RoleAccept {
				result, run, artifact, err = readSharedAcceptanceResult(ctx, tx.Q(), r, &inputRound)
				last.AcceptanceID, last.AcceptanceVerdict = inputRound.AcceptanceID, inputRound.AcceptanceVerdict
			} else {
				result, run, artifact, err = readSharedReviewResult(ctx, tx.Q(), r, inputRound)
			}
			if errors.Is(err, errAcceptanceReviewEvidence) {
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
			result, run, artifact, err := readSharedReviewResult(ctx, tx.Q(), r, correction)
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
			if p.ReviewerTaskID == last.TaskID {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "supplement requires a distinct immutable evidence task")
			}
			bound, err := bindSharedReviewTask(ctx, tx.Q(), r, p.ReviewerTaskID, "", last.ExecutionRequirements)
			if err != nil {
				return Outcome{}, err
			}
			if bound.Reviewer != r.Reviewer {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only the original formal reviewer may supplement its evidence")
			}
			last.SupplementTaskID, last.SupplementSpecVersion = bound.TaskID, bound.SpecVersion
		case "assess":
			if r.Status != "passed" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only a passed review can be confirmed applicable")
			}
			observation, err := store.GetCurrentDeliveryObservation(ctx, tx.Q(), i.IssueID, ref)
			if err != nil {
				return Outcome{}, err
			}
			if observation.Observation.ErrorKind != "" || observation.Observation.RefreshPending || observation.Observation.HeadSHA != r.HeadSHA || observation.Observation.BaseSHA != r.BaseSHA {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "a changed candidate requires the original reviewer's focused recheck")
			}
			digest, err := store.SharedReviewRequirements(ctx, tx.Q(), r)
			if err != nil {
				return Outcome{}, err
			}
			r.AssessedRequirementsDigest, r.AssessmentReason = digest, p.Reason
		case "resolve":
			if r.Status != "changes_requested" && r.Status != "escalated" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only pending review findings accept dispositions")
			}
			if err := validateReviewDispositions(r, p.Dispositions); err != nil {
				return Outcome{}, err
			}
			r.Dispositions = slices.Clone(p.Dispositions)
		default:
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review action must be link, unlink, start, record, correct, supplement, resolve, assess, recheck or authorize_recheck")
		}
	}
	if len(r.Rounds) == 0 {
		// Expose the exact input before the owner creates the reviewer task, so
		// its frozen instructions can name the real review and requirements.
		r.RequirementsDigest, err = store.SharedReviewRequirements(ctx, tx.Q(), r)
		if err != nil {
			return Outcome{}, err
		}
		o, err := store.GetCurrentDeliveryObservation(ctx, tx.Q(), i.IssueID, ref)
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
		if spec.AgentSelection == nil || spec.AgentSelection.Implementation.AgentID == "" {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "shared review requires an explicitly named reviewer task")
		}
		out.Reviewer = spec.AgentSelection.Implementation.AgentID
		if !spec.RequiresDelivery("report") {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "reviewer must submit an immutable report artifact")
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
		rows, err := store.FollowupIDs(ctx, q, `SELECT run_id FROM runs WHERE task_id=? AND role='implement'`, link.TaskID)
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
	return out, nil
}

func readSharedReviewResult(ctx context.Context, q store.Querier, r protocol.SharedDeliveryReview, round protocol.SharedDeliveryReviewRound) (*protocol.SharedReviewResult, string, string, error) {
	t, err := store.GetTask(ctx, q, round.TaskID)
	if err != nil {
		return nil, "", "", err
	}
	if t.SpecVersion != round.SpecVersion || t.Lifecycle != protocol.TaskCompleted || t.CurrentArtifactID == "" {
		return nil, "", "", protocol.Errorf(protocol.CodeInvalidTransition, "the bound review task has not completed under its frozen agreement")
	}
	a, err := store.GetArtifact(ctx, q, t.CurrentArtifactID)
	if err != nil {
		return nil, "", "", err
	}
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(a.RefJSON), &artifact); err != nil {
		return nil, "", "", err
	}
	var result *protocol.SharedReviewResult
	run := a.RunID
	if round.AcceptanceID != "" {
		verdicts, err := store.ListAcceptances(ctx, q, t.TaskID, a.ArtifactID)
		if err != nil {
			return nil, "", "", err
		}
		for _, v := range verdicts {
			if v.AcceptanceID == round.AcceptanceID && v.SpecVersion == round.SpecVersion && v.Verdict == protocol.VerdictPassed {
				matched := false
				for _, m := range artifact.Mounts {
					matched = matched || m.CommitSHA == r.HeadSHA && m.SnapshotSHA == ""
				}
				if !matched || v.EvidenceJSON == "" || v.EvidenceJSON == "null" || v.EvidenceJSON == "{}" {
					return nil, "", "", protocol.Errorf(protocol.CodeInvalidRequest, "reuse requires a real candidate-matching acceptance with repository review evidence")
				}
				var evidence struct {
					DeliveryReview *protocol.SharedReviewEvidence `json:"delivery_review"`
				}
				if err := json.Unmarshal([]byte(v.EvidenceJSON), &evidence); err != nil {
					return nil, "", "", err
				}
				original := evidence.DeliveryReview
				if original == nil || original.Provider != r.Provider || original.Repository != r.Repository || original.Number != r.Number || original.HeadSHA != round.HeadSHA || original.BaseSHA != round.BaseSHA || original.RequirementsDigest != round.Requirements || strings.TrimSpace(original.RepositoryCheck) == "" {
					return nil, "", "", protocol.Errorf(protocol.CodeInvalidRequest, "the original reviewer did not establish this complete PR candidate and requirement scope; retain that verdict and request focused evidence from the same reviewer")
				}
				result = &protocol.SharedReviewResult{ReviewID: r.ReviewID, HeadSHA: original.HeadSHA, BaseSHA: original.BaseSHA, RequirementsDigest: original.RequirementsDigest, Verdict: "passed", RepositoryCheck: original.RepositoryCheck}
				run = v.RunID
			}
		}
	} else if artifact.Report != nil {
		result = artifact.Report.SharedReview
	}
	if err := validateSharedReviewResult(result, r, round); err != nil {
		return nil, "", "", err
	}
	pin, err := store.GetRunExecution(ctx, q, run)
	if err != nil {
		return nil, "", "", err
	}
	if round.Reviewer != pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID {
		return nil, "", "", protocol.Errorf(protocol.CodeForbidden, "review artifact was produced by a different reviewer")
	}
	return result, run, a.ArtifactID, nil
}

func validateSharedReviewResult(result *protocol.SharedReviewResult, r protocol.SharedDeliveryReview, round protocol.SharedDeliveryReviewRound) error {
	if result == nil || result.ReviewID != r.ReviewID || result.HeadSHA != round.HeadSHA || result.BaseSHA != round.BaseSHA || result.RequirementsDigest != round.Requirements || strings.TrimSpace(result.RepositoryCheck) == "" || (result.Verdict != "passed" && result.Verdict != "changes_requested" && result.Verdict != "blocked" && result.Verdict != "inconclusive") || (result.Verdict == "passed" && len(result.Findings) > 0) || (result.Verdict == "changes_requested" && len(result.Findings) == 0) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "review report must bind the complete candidate, requirement digest, repository check and actual verdict")
	}
	seen := map[string]bool{}
	for _, f := range result.Findings {
		if f.ID == "" || seen[f.ID] || strings.TrimSpace(f.Body) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "review findings require distinct IDs and concrete descriptions")
		}
		seen[f.ID] = true
	}
	return nil
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
