package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

var errAcceptanceReviewEvidence = errors.New("actual acceptance needs original-reviewer whole-PR evidence")

// Acceptance prebinding names an already selected real reviewer, not the source
// task's implementer. The exact artifact and actual execution profile are frozen.
func bindSharedAcceptanceReview(ctx context.Context, q store.Querier, r protocol.SharedDeliveryReview, task, acceptance string) (protocol.SharedDeliveryReviewRound, error) {
	if acceptance != "" {
		out, err := bindSharedReviewTask(ctx, q, r, task, acceptance)
		if err != nil {
			return out, err
		}
		pin, err := store.GetRunExecution(ctx, q, out.RunID)
		if err != nil {
			return out, err
		}
		out.ReviewerRole, out.ReviewerProfile = protocol.RoleAccept, &pin.Profile
		if err := acceptanceArtifactMatches(ctx, q, out.ArtifactID, r.HeadSHA); err != nil {
			return out, err
		}
		return out, nil
	}
	out := protocol.SharedDeliveryReviewRound{TaskID: task, ReviewerRole: protocol.RoleAccept}
	t, spec, err := loadTask(ctx, q, task)
	if err != nil {
		return out, err
	}
	out.SpecVersion, out.ArtifactID = t.SpecVersion, t.CurrentArtifactID
	if out.ArtifactID == "" {
		return out, protocol.Errorf(protocol.CodeInvalidTransition, "acceptance review requires the task's submitted immutable artifact")
	}
	runs, err := store.FollowupIDs(ctx, q, `SELECT r.run_id FROM runs r JOIN plan_actions a USING(action_id) WHERE r.task_id=? AND r.role='accept' AND a.target_spec_version=? AND a.target_artifact_id=? ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, task, t.SpecVersion, t.CurrentArtifactID)
	if err != nil {
		return out, err
	}
	if len(runs) == 0 {
		return out, protocol.Errorf(protocol.CodeInvalidTransition, "acceptance reviewer has not yet been selected for this exact task, spec and artifact")
	}
	if err := acceptanceArtifactMatches(ctx, q, out.ArtifactID, r.HeadSHA); err != nil {
		return out, err
	}
	out.RunID = runs[0]
	pin, err := store.GetRunExecution(ctx, q, out.RunID)
	if err != nil {
		return out, err
	}
	out.Reviewer = pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
	out.ReviewerProfile = &pin.Profile
	out.ExecutionRequirements, err = store.SharedReviewExecutionRequirements(ctx, q, r)
	if err != nil {
		return out, err
	}
	if err := store.RequireSharedReviewExecutionProfile(out.ExecutionRequirements, pin.Profile); err != nil {
		return out, err
	}
	for _, link := range r.Links {
		if link.TaskID == "" {
			continue
		}
		_, sourceSpec, err := loadTask(ctx, q, link.TaskID)
		if err != nil {
			return out, err
		}
		if sourceSpec.AgentSelection != nil {
			required := sourceSpec.AgentSelection.Acceptance
			if required.AgentID != "" && required.AgentID != out.Reviewer || required.CategoryID != "" && (spec.AgentSelection == nil || spec.AgentSelection.Acceptance.CategoryID != required.CategoryID) {
				return out, protocol.Errorf(protocol.CodeForbidden, "retain the linked delivery's actual formal acceptance reviewer and category")
			}
		}
		ids, err := store.FollowupIDs(ctx, q, `SELECT run_id FROM runs WHERE task_id=? AND role='implement'`, link.TaskID)
		if err != nil {
			return out, err
		}
		for _, id := range ids {
			impl, err := store.GetRunExecution(ctx, q, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			if out.Reviewer == impl.Identity.ExecutionEnvironmentID+"/"+impl.Identity.LocalID {
				return out, protocol.Errorf(protocol.CodeForbidden, "the formal reviewer must be independent of the linked implementation")
			}
		}
	}
	return out, nil
}

// Read only immutable acceptance facts for the frozen artifact. Current task
// lifecycle/spec can have moved after a real changes_required result.
func readSharedAcceptanceResult(ctx context.Context, q store.Querier, r protocol.SharedDeliveryReview, round *protocol.SharedDeliveryReviewRound) (*protocol.SharedReviewResult, string, string, error) {
	if err := acceptanceArtifactMatches(ctx, q, round.ArtifactID, round.HeadSHA); err != nil {
		return nil, "", "", err
	}
	rows, err := store.ListAcceptances(ctx, q, round.TaskID, round.ArtifactID)
	if err != nil {
		return nil, "", "", err
	}
	for _, v := range rows {
		if v.SpecVersion != round.SpecVersion || round.AcceptanceID != "" && v.AcceptanceID != round.AcceptanceID {
			continue
		}
		// A new round cannot import an older attempt on the same artifact.
		// A later continuation of the preflight Run remains eligible, but its
		// actual role, artifact, reviewer and frozen profile are still checked.
		var currentAttempt bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs actual JOIN runs frozen ON frozen.run_id=? WHERE actual.run_id=? AND actual.rowid>=frozen.rowid)`, round.RunID, v.RunID).Scan(&currentAttempt); err != nil {
			return nil, "", "", err
		}
		if !currentAttempt {
			continue
		}
		reused := false
		for _, previous := range r.Rounds {
			if previous.Number < round.Number && (previous.RunID == v.RunID || previous.AcceptanceID == v.AcceptanceID) {
				reused = true
			}
		}
		if reused {
			continue
		}
		run, err := store.GetRun(ctx, q, v.RunID)
		if err != nil {
			return nil, "", "", err
		}
		action, err := store.GetAction(ctx, q, run.ActionID)
		if err != nil {
			return nil, "", "", err
		}
		if run.Role != protocol.RoleAccept || run.TaskID != round.TaskID || action.TargetSpecVersion != round.SpecVersion || action.TargetArtifactID != round.ArtifactID {
			return nil, "", "", protocol.Errorf(protocol.CodeForbidden, "acceptance must originate from the frozen task, spec, artifact and acceptance role")
		}
		pin, err := store.GetRunExecution(ctx, q, v.RunID)
		if err != nil {
			return nil, "", "", err
		}
		if round.Reviewer != pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID {
			return nil, "", "", protocol.Errorf(protocol.CodeForbidden, "acceptance was produced by a different reviewer")
		}
		round.AcceptanceID, round.AcceptanceVerdict = v.AcceptanceID, v.Verdict
		var evidence struct {
			DeliveryReview *protocol.SharedReviewEvidence `json:"delivery_review"`
		}
		evidenceErr := json.Unmarshal([]byte(v.EvidenceJSON), &evidence)
		original := evidence.DeliveryReview
		result := &protocol.SharedReviewResult{ReviewID: r.ReviewID, HeadSHA: round.HeadSHA, BaseSHA: round.BaseSHA, RequirementsDigest: round.Requirements, Verdict: string(v.Verdict)}
		if v.Verdict == protocol.VerdictPassed || v.Verdict == protocol.VerdictChangesRequired {
			if evidenceErr != nil || original == nil || original.Provider != r.Provider || original.Repository != r.Repository || original.Number != r.Number || original.HeadSHA != round.HeadSHA || original.BaseSHA != round.BaseSHA || original.RequirementsDigest != round.Requirements || strings.TrimSpace(original.RepositoryCheck) == "" {
				return nil, v.RunID, v.ArtifactID, errAcceptanceReviewEvidence
			}
			result.RepositoryCheck, result.Findings = original.RepositoryCheck, original.Findings
			if v.Verdict == protocol.VerdictPassed {
				result.Verdict = "passed"
			} else {
				result.Verdict = "changes_requested"
				if len(result.Findings) == 0 {
					var issues []string
					if err := json.Unmarshal([]byte(v.IssuesJSON), &issues); err == nil {
						for n, body := range issues {
							result.Findings = append(result.Findings, protocol.SharedReviewFinding{ID: fmt.Sprintf("%s-%d", v.AcceptanceID, n+1), Body: body})
						}
					}
				}
			}
			if err := validateSharedReviewResult(result, r, *round); err != nil {
				return nil, v.RunID, v.ArtifactID, fmt.Errorf("%w: %v", errAcceptanceReviewEvidence, err)
			}
		} else if v.Verdict != protocol.VerdictBlocked && v.Verdict != protocol.VerdictInconclusive {
			return nil, "", "", protocol.Errorf(protocol.CodeInvalidRequest, "actual acceptance verdict is unknown")
		}
		return result, v.RunID, v.ArtifactID, nil
	}
	return nil, "", "", protocol.Errorf(protocol.CodeInvalidTransition, "await the original reviewer's actual acceptance for the frozen artifact; a preflight or ended run is not a verdict")
}

func acceptanceArtifactMatches(ctx context.Context, q store.Querier, artifact, head string) error {
	a, err := store.GetArtifact(ctx, q, artifact)
	if err != nil {
		return err
	}
	var payload protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(a.RefJSON), &payload); err != nil {
		return err
	}
	for _, m := range payload.Mounts {
		if m.CommitSHA == head && m.SnapshotSHA == "" {
			return nil
		}
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, "acceptance artifact does not match the frozen PR candidate")
}
