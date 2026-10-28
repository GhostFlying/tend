package views

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// SchedulingCandidates decodes the same facts for claims and execution views.
func SchedulingCandidates(rows []store.CandidateRow) ([]scheduler.Candidate, error) {
	out := make([]scheduler.Candidate, 0, len(rows))
	for _, r := range rows {
		var spec domain.Spec
		if err := json.Unmarshal([]byte(r.SpecJSON), &spec); err != nil {
			return nil, fmt.Errorf("views: decode spec of %s: %w", r.Action.TaskID, err)
		}
		out = append(out, scheduler.Candidate{
			ActionID: r.Action.ActionID, TaskID: r.Action.TaskID, Kind: r.Action.Kind, Status: r.Action.Status,
			Generation: r.Action.Generation, TargetSpecVersion: r.Action.TargetSpecVersion,
			TargetArtifactID: r.Action.TargetArtifactID, CreatedAt: r.Action.CreatedAt,
			Lifecycle: r.Lifecycle, Control: r.Control, Block: r.Block, SpecVersion: r.SpecVersion,
			ImplementGeneration: r.ImplementGeneration, CurrentArtifactID: r.CurrentArtifactID, Rating: r.Rating, Weight: r.Weight,
			DependenciesDone: r.DependenciesDone, Requirements: spec.Requirements, Repos: spec.Workspace.Repos(), Mounts: len(spec.Workspace.Mounts),
			RequiresAcceptanceExecution: spec.AcceptanceExecution != nil, RequiresObservation: spec.Observation != nil, RequiresTaskInputs: len(spec.Inputs) > 0, WorkspaceMode: spec.Workspace.ModeValue(), RequiresWorkspaceProtocol: spec.Workspace.ModeValue() != domain.WorkspaceNew || len(spec.Deliverables) > 0,
			WorkspaceHost: r.WorkspaceHost, WorkspaceOccupied: r.WorkspaceOccupied, AbandonedHosts: r.AbandonedHosts,
		})
	}
	return out, nil
}

// ApplyIssueDependencies applies current plan prerequisites to scheduling facts.
func ApplyIssueDependencies(ctx context.Context, q store.Querier, candidates []scheduler.Candidate) error {
	for i := range candidates {
		c := &candidates[i]
		items, err := store.ListTaskIssues(ctx, q, c.TaskID)
		if err != nil {
			return err
		}
		var frozen struct {
			SharedContext []protocol.TaskWorkContext `json:"shared_context"`
		}
		if c.SpecVersion > 0 {
			raw, err := store.GetSpec(ctx, q, c.TaskID, c.SpecVersion)
			if err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
				return err
			}
		}
		for _, item := range items {
			// Only registered new-message responsibility gates dispatch; historical
			// tasks without a D112 classification are not backfilled.
			var tracked bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_context_obligations WHERE work_item_id=?) OR EXISTS(SELECT 1 FROM logical_turn_contexts WHERE work_item_id=?)`, item.WorkItemID, item.WorkItemID).Scan(&tracked); err != nil {
				return err
			}
			for _, binding := range frozen.SharedContext {
				tracked = tracked || (binding.WorkItemID == item.WorkItemID && binding.ContextDigest != "")
			}
			if tracked {
				ready, err := store.WorkItemContextReady(ctx, q, item.WorkItemID)
				if err != nil {
					return err
				}
				c.ContextPending = c.ContextPending || !ready
			}
			for _, binding := range frozen.SharedContext {
				if binding.WorkItemID == item.WorkItemID && binding.ContextDigest != "" {
					digest, _, err := store.EffectiveContext(ctx, q, item.WorkItemID)
					if err != nil {
						return err
					}
					c.ContextPending = c.ContextPending || binding.ContextDigest != digest
				}
			}
			if item.Status != "open" {
				c.DependenciesDone = false
				continue
			}
			stages := map[string]protocol.WorkItemStage{}
			for _, stage := range item.Stages {
				stages[stage.StageID] = stage
			}
			for _, stage := range item.Stages {
				if !slices.Contains(stage.TaskIDs, c.TaskID) {
					continue
				}
				if stage.Status == "retired" || stage.Status == "cancelled" {
					c.DependenciesDone = false
				}
				// A child inherits the prerequisites of every containing group.
				for ancestor := stage; ; ancestor = stages[ancestor.ParentID] {
					if ancestor.Status == "retired" || ancestor.Status == "cancelled" {
						c.DependenciesDone = false
					}
					for _, dependency := range ancestor.DependsOn {
						dep := stages[dependency]
						if dep.Status != "done" && !slices.Contains(dep.TaskIDs, c.TaskID) {
							c.DependenciesDone = false
							if c.DependencyTaskID == "" {
								c.DependencyTaskID, err = unfinishedStageTask(ctx, q, item, dep.StageID, c.TaskID)
								if err != nil {
									return err
								}
							}
						}
					}
					if ancestor.ParentID == "" {
						break
					}
				}
			}
		}
	}
	return nil
}

// unfinishedStageTask supplies navigation only when a prerequisite contains an
// actual unfinished task. A stage awaiting manual completion has no invented
// task blocker; a completed task is not evidence that the stage is complete.
func unfinishedStageTask(ctx context.Context, q store.Querier, item protocol.WorkItem, stageID, dependentTask string) (string, error) {
	stages := map[string]protocol.WorkItemStage{}
	for _, stage := range item.Stages {
		stages[stage.StageID] = stage
	}
	for _, stage := range item.Stages {
		belongs := stage.StageID == stageID
		for parent := stage.ParentID; parent != "" && !belongs; parent = stages[parent].ParentID {
			belongs = parent == stageID
		}
		if !belongs {
			continue
		}
		for _, id := range stage.TaskIDs {
			if id == dependentTask {
				continue
			}
			task, err := store.GetTask(ctx, q, id)
			if err != nil {
				return "", err
			}
			if task.Lifecycle != protocol.TaskCompleted {
				return id, nil
			}
		}
	}
	return "", nil
}
