package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

var ErrAcceptanceReviewEvidence = errors.New("actual acceptance needs original-reviewer whole-PR evidence")

func ReadSharedReviewResult(ctx context.Context, q Querier, r protocol.SharedDeliveryReview, round protocol.SharedDeliveryReviewRound) (*protocol.SharedReviewResult, string, string, error) {
	t, err := GetTask(ctx, q, round.TaskID)
	if err != nil {
		return nil, "", "", err
	}
	artifactID, err := frozenSharedReviewArtifact(ctx, q, t, round)
	if err != nil {
		return nil, "", "", err
	}
	a, err := GetArtifact(ctx, q, artifactID)
	if err != nil {
		return nil, "", "", err
	}
	runRow, err := GetRun(ctx, q, a.RunID)
	if err != nil {
		return nil, "", "", err
	}
	action, err := GetAction(ctx, q, runRow.ActionID)
	if err != nil {
		return nil, "", "", err
	}
	if a.TaskID != round.TaskID || runRow.TaskID != round.TaskID || action.TaskID != round.TaskID || action.TargetSpecVersion != round.SpecVersion || round.ImplementGeneration > 0 && action.Generation != round.ImplementGeneration || round.RunID != "" && round.RunID != a.RunID && round.AcceptanceID == "" {
		return nil, "", "", protocol.Errorf(protocol.CodeInvalidTransition, "the report must come from this round's frozen Task execution")
	}
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(a.RefJSON), &artifact); err != nil {
		return nil, "", "", err
	}
	var result *protocol.SharedReviewResult
	run := a.RunID
	if round.AcceptanceID != "" {
		verdicts, err := ListAcceptances(ctx, q, t.TaskID, a.ArtifactID)
		if err != nil {
			return nil, "", "", err
		}
		for _, v := range verdicts {
			if v.AcceptanceID == round.AcceptanceID && v.SpecVersion == round.SpecVersion && v.Verdict == protocol.VerdictPassed {
				matched := false
				for _, m := range artifact.Mounts {
					matched = matched || m.CommitSHA == round.HeadSHA && m.SnapshotSHA == ""
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
	pin, err := GetRunExecution(ctx, q, run)
	if err != nil {
		return nil, "", "", err
	}
	if round.Reviewer != pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID {
		return nil, "", "", protocol.Errorf(protocol.CodeForbidden, "review artifact was produced by a different reviewer")
	}
	return result, run, a.ArtifactID, nil
}

// Task continuation can replace current_artifact_id. Resolve a prebound report
// through its immutable execution, never through a newer Task generation.
func frozenSharedReviewArtifact(ctx context.Context, q Querier, task TaskRow, round protocol.SharedDeliveryReviewRound) (string, error) {
	if round.ArtifactID != "" {
		return round.ArtifactID, nil
	}
	if round.ImplementGeneration == 0 && round.RunID == "" {
		return "", protocol.Errorf(protocol.CodeUnknownOutcome, "the frozen review execution identity is missing; retain its original evidence before recovery")
	}
	const frozen = ` FROM artifacts f JOIN runs r ON r.run_id=f.run_id JOIN plan_actions a ON a.action_id=r.action_id WHERE f.task_id=? AND r.task_id=f.task_id AND a.task_id=f.task_id AND a.target_spec_version=? AND (?=0 OR a.generation=?) AND (?='' OR r.run_id=?)`
	args := []any{round.TaskID, round.SpecVersion, round.ImplementGeneration, round.ImplementGeneration, round.RunID, round.RunID}
	// The existing completion fact distinguishes an actual result from a
	// retained stale submission. Current-completed historical rows keep their
	// original contract, still restricted by the full immutable tuple.
	current := task.Lifecycle == protocol.TaskCompleted && task.SpecVersion == round.SpecVersion
	ids, err := FollowupIDs(ctx, q, `SELECT f.artifact_id`+frozen+` AND (EXISTS(SELECT 1 FROM events e WHERE e.object_type='task' AND e.object_id=f.task_id AND e.kind='task.completed' AND json_extract(e.body_json,'$.artifact_id')=f.artifact_id AND json_extract(e.body_json,'$.spec_version')=a.target_spec_version) OR (? AND f.artifact_id=?)) LIMIT 2`, append(args, current, task.CurrentArtifactID)...)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		var submitted bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+frozen+`)`, args...).Scan(&submitted); err != nil {
			return "", err
		}
		if submitted {
			return "", protocol.Errorf(protocol.CodeUnknownOutcome, "the frozen report's completion is unconfirmed; retain its original result")
		}
		return "", protocol.Errorf(protocol.CodeInvalidTransition, "the frozen review execution has not produced its immutable report")
	}
	if len(ids) != 1 {
		return "", protocol.Errorf(protocol.CodeUnknownOutcome, "multiple immutable results match the frozen review execution; reconcile their original identities")
	}
	return ids[0], nil
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

func ReadSharedAcceptanceResult(ctx context.Context, q Querier, r protocol.SharedDeliveryReview, round *protocol.SharedDeliveryReviewRound) (*protocol.SharedReviewResult, string, string, error) {
	if err := SharedAcceptanceArtifactMatches(ctx, q, round.ArtifactID, round.HeadSHA); err != nil {
		return nil, "", "", err
	}
	rows, err := ListAcceptances(ctx, q, round.TaskID, round.ArtifactID)
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
		run, err := GetRun(ctx, q, v.RunID)
		if err != nil {
			return nil, "", "", err
		}
		action, err := GetAction(ctx, q, run.ActionID)
		if err != nil {
			return nil, "", "", err
		}
		if run.Role != protocol.RoleAccept || run.TaskID != round.TaskID || action.TargetSpecVersion != round.SpecVersion || action.TargetArtifactID != round.ArtifactID {
			return nil, "", "", protocol.Errorf(protocol.CodeForbidden, "acceptance must originate from the frozen task, spec, artifact and acceptance role")
		}
		pin, err := GetRunExecution(ctx, q, v.RunID)
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
				return nil, v.RunID, v.ArtifactID, ErrAcceptanceReviewEvidence
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
				return nil, v.RunID, v.ArtifactID, fmt.Errorf("%w: %v", ErrAcceptanceReviewEvidence, err)
			}
		} else if v.Verdict != protocol.VerdictBlocked && v.Verdict != protocol.VerdictInconclusive {
			return nil, "", "", protocol.Errorf(protocol.CodeInvalidRequest, "actual acceptance verdict is unknown")
		}
		return result, v.RunID, v.ArtifactID, nil
	}
	return nil, "", "", protocol.Errorf(protocol.CodeInvalidTransition, "await the original reviewer's actual acceptance for the frozen artifact; a preflight or ended run is not a verdict")
}

func SharedAcceptanceArtifactMatches(ctx context.Context, q Querier, artifact, head string) error {
	a, err := GetArtifact(ctx, q, artifact)
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
