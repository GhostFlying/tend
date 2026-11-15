package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func readPredeploymentArtifact(ctx context.Context, q store.Querier, task store.TaskRow, id string) (protocol.ArtifactPayload, *protocol.PredeploymentResult, error) {
	var payload protocol.ArtifactPayload
	if id == "" {
		return payload, nil, nil
	}
	a, err := store.GetArtifact(ctx, q, id)
	if err != nil {
		return payload, nil, err
	}
	if a.TaskID != task.TaskID {
		return payload, nil, protocol.Errorf(protocol.CodeForbidden, "stage evidence belongs to another Task")
	}
	run, err := store.GetRun(ctx, q, a.RunID)
	if err != nil {
		return payload, nil, err
	}
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return payload, nil, err
	}
	if task.Kind != protocol.TaskKindIssueManager && action.TargetSpecVersion != task.SpecVersion {
		return payload, nil, protocol.Errorf(protocol.CodeStaleRevision, "stage evidence belongs to another specification")
	}
	if err := json.Unmarshal([]byte(a.RefJSON), &payload); err != nil {
		return payload, nil, err
	}
	if payload.Report == nil {
		return payload, nil, nil
	}
	return payload, payload.Report.Predeployment, nil
}

// A negative verdict clears the lifecycle pointer, not the original evidence.
// Only its real Task/spec acceptance supplies a preparation repair source.
func preparationStage(ctx context.Context, q store.Querier, task store.TaskRow) (string, *protocol.PredeploymentResult, bool, error) {
	id := task.CurrentArtifactID
	if id == "" {
		accs, err := store.ListAcceptances(ctx, q, task.TaskID, "")
		if err != nil {
			return "", nil, false, err
		}
		for n := len(accs) - 1; n >= 0; n-- {
			if accs[n].SpecVersion == task.SpecVersion {
				id = accs[n].ArtifactID
				break
			}
		}
	}
	_, stage, err := readPredeploymentArtifact(ctx, q, task, id)
	if err != nil || stage == nil {
		return id, stage, false, err
	}
	accs, err := store.ListAcceptances(ctx, q, task.TaskID, id)
	if err != nil {
		return id, stage, false, err
	}
	for n := len(accs) - 1; n >= 0; n-- {
		if accs[n].SpecVersion == task.SpecVersion {
			return id, stage, accs[n].Verdict != protocol.VerdictPassed, nil
		}
	}
	return id, stage, false, nil
}

// Installation-report repairs keep the most recent stage for this frozen
// candidate, not whichever artifact happens to be the current task pointer.
// An unaccepted matching stage must not fall back to an older passed one.
func acceptedStageForCandidate(ctx context.Context, q store.Querier, task store.TaskRow, candidate protocol.IssueDeliveryCandidate) (protocol.ArtifactPayload, *protocol.PredeploymentResult, error) {
	artifacts, err := store.ListArtifactsByTask(ctx, q, task.TaskID)
	if err != nil {
		return protocol.ArtifactPayload{}, nil, err
	}
	for n := len(artifacts) - 1; n >= 0; n-- {
		payload, stage, err := readPredeploymentArtifact(ctx, q, task, artifacts[n].ArtifactID)
		if protocol.CodeOf(err) == protocol.CodeStaleRevision {
			continue
		}
		if err != nil {
			return payload, stage, err
		}
		if stage == nil || stage.DeliveryID != candidate.DeliveryID || stage.HeadSHA != candidate.HeadSHA || stage.BaseSHA != candidate.BaseSHA {
			continue
		}
		if _, err := acceptedPredeploymentArtifact(ctx, q, task, artifacts[n].ArtifactID); err != nil {
			return payload, stage, err
		}
		return payload, stage, nil
	}
	return protocol.ArtifactPayload{}, nil, nil
}

func requireInstallationStage(ctx context.Context, q store.Querier, task store.TaskRow, candidate protocol.IssueDeliveryCandidate) error {
	id, stage, rejected, err := preparationStage(ctx, q, task)
	if err != nil || stage == nil {
		return err
	}
	if rejected || stage.DeliveryID != candidate.DeliveryID || stage.HeadSHA != candidate.HeadSHA || stage.BaseSHA != candidate.BaseSHA {
		return protocol.Errorf(protocol.CodeInvalidTransition, "finish the candidate correction and bind its accepted stage before installation")
	}
	_, err = acceptedPredeploymentArtifact(ctx, q, task, id)
	return err
}
