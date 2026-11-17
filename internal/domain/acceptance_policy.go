package domain

import "github.com/GhostFlying/tend/internal/protocol"

// ChangeAcceptanceRequirement changes an active agreement, never its historical
// results. A withdrawn review completes on the actual artifact without a verdict.
func ChangeAcceptanceRequirement(task Task, required bool) (Decision, error) {
	if task.Lifecycle.Terminal() {
		return Decision{}, errTransition(task, "change review of")
	}
	task.ControlRevision++
	d := Decision{Task: task, Events: []EventDraft{{Kind: "task.review_choice_changed", Body: map[string]any{"independent_acceptance_required": required}}}}
	if !required && task.Lifecycle == protocol.TaskAwaitingAcceptance && task.CurrentArtifactID != "" {
		d.Task.Lifecycle, d.Task.Block = protocol.TaskCompleted, protocol.BlockNone
		d.CancelOpenActions = true
		d.Events = append(d.Events, EventDraft{Kind: "task.completed", Body: map[string]any{"artifact_id": task.CurrentArtifactID, "spec_version": task.SpecVersion, "independent_acceptance_required": false}})
	}
	return d, nil
}
