package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// completedTaskEvidence names the actual basis of completion. Waiving a
// separate verdict cannot manufacture one or verify a provider/deployment.
func completedTaskEvidence(ctx context.Context, q store.Querier, taskID, summary string) (protocol.IssueEvidence, error) {
	task, err := store.GetTask(ctx, q, taskID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	if task.Lifecycle != protocol.TaskCompleted {
		return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "a current completed result is required")
	}
	accs, err := store.ListAcceptances(ctx, q, taskID, task.CurrentArtifactID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	for _, a := range accs {
		if a.SpecVersion == task.SpecVersion && a.Verdict == protocol.VerdictPassed {
			return protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceAcceptance, Reference: a.AcceptanceID, Summary: summary}, nil
		}
	}
	policy, err := store.TaskAcceptancePolicy(ctx, q, taskID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	var current bool
	if policy != nil && !policy.Required {
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts a JOIN runs r USING(run_id) JOIN plan_actions action ON action.action_id=r.action_id WHERE a.artifact_id=? AND a.task_id=? AND action.target_spec_version=?)`, task.CurrentArtifactID, taskID, task.SpecVersion).Scan(&current); err != nil {
			return protocol.IssueEvidence{}, err
		}
	}
	if !current {
		return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "matching independent acceptance or frozen creator-authorized result is required")
	}
	return protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceResult, Reference: task.CurrentArtifactID, Summary: summary}, nil
}
