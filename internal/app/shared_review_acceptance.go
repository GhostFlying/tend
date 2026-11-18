package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

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
		if err := store.SharedAcceptanceArtifactMatches(ctx, q, out.ArtifactID, r.HeadSHA); err != nil {
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
	if err := store.SharedAcceptanceArtifactMatches(ctx, q, out.ArtifactID, r.HeadSHA); err != nil {
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
		ids, err := store.FollowupIDs(ctx, q, `SELECT run_id FROM runs WHERE task_id=? AND role IN ('implement','issue_manager')`, link.TaskID)
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
