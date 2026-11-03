package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// taskRuleQuestionOrigin freezes the authoritative spec at rule evaluation.
// Only a real triggering run may supply action provenance. A latest-run guess
// would attach unrelated questions to whichever execution happened to finish.
func taskRuleQuestionOrigin(ctx context.Context, x *Exec, q store.Querier, task store.TaskRow, question domain.DecisionDraft, trigger map[string]any) (store.TaskRuleQuestionOrigin, error) {
	origin := store.TaskRuleQuestionOrigin{Format: 1, TaskID: task.TaskID, SpecVersion: task.SpecVersion}
	for _, effect := range question.Effects {
		if effect == protocol.EffectImplement {
			origin.ImplementGeneration = task.ImplementGeneration
		}
		if effect == protocol.EffectAccept {
			origin.ArtifactID = task.CurrentArtifactID
		}
	}
	run := ""
	if x.Env.Target.Type == "run" {
		run = x.Env.Target.ID
	} else if trigger != nil {
		run, _ = trigger["run_id"].(string)
	}
	if run == "" {
		return origin, nil
	}
	row, err := store.GetRun(ctx, q, run)
	if err != nil {
		return origin, err
	}
	if row.TaskID != task.TaskID {
		return origin, protocol.Errorf(protocol.CodeInvalidTransition, "task question trigger belongs to another task")
	}
	action, err := store.GetAction(ctx, q, row.ActionID)
	if err != nil {
		return origin, err
	}
	if action.TaskID == task.TaskID && action.TargetSpecVersion == task.SpecVersion {
		origin.SourceActionID = action.ActionID
		if (origin.ImplementGeneration > 0 && action.Kind == protocol.ActionImplement && action.Generation == origin.ImplementGeneration) || (origin.ArtifactID != "" && action.Kind == protocol.ActionAccept && action.TargetArtifactID == origin.ArtifactID) {
			origin.ActionID = action.ActionID
		}
	}
	return origin, nil
}
