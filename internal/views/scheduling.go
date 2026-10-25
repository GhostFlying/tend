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
			RequiresTaskInputs: len(spec.Inputs) > 0, WorkspaceMode: spec.Workspace.ModeValue(), RequiresWorkspaceProtocol: spec.Workspace.ModeValue() != domain.WorkspaceNew || len(spec.Deliverables) > 0,
			WorkspaceHost: r.WorkspaceHost, WorkspaceOccupied: r.WorkspaceOccupied, AbandonedHosts: r.AbandonedHosts,
		})
	}
	return out, nil
}

// ApplyWorkItemDependencies applies current plan prerequisites to scheduling facts.
func ApplyWorkItemDependencies(ctx context.Context, q store.Querier, candidates []scheduler.Candidate) error {
	for i := range candidates {
		c := &candidates[i]
		items, err := store.ListTaskWorkItems(ctx, q, c.TaskID)
		if err != nil {
			return err
		}
		for _, item := range items {
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
