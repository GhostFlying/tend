package app

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

// QueueOrder is the order claims rank queued work in as of now (D60).
func (a *App) QueueOrder() scheduler.Order {
	return scheduler.Order{Now: a.now().UnixMilli(), WaitPerHour: a.waitPer}
}

// Queue lists the queued actions that wait only for a host, in the order
// claims take them. A host passes over the ones it cannot run.
func Queue(ctx context.Context, q store.Querier, o scheduler.Order) ([]protocol.QueueEntry, error) {
	rows, err := store.ListClaimCandidates(ctx, q)
	if err != nil {
		return nil, err
	}
	candidates, err := candidatesOf(rows)
	if err != nil {
		return nil, err
	}
	tasks := make(map[string]store.CandidateRow, len(rows))
	for _, r := range rows {
		tasks[r.Action.TaskID] = r
	}
	candidates = slices.DeleteFunc(candidates, func(c scheduler.Candidate) bool { return scheduler.Held(c) != "" })
	slices.SortFunc(candidates, func(a, b scheduler.Candidate) int { return scheduler.Compare(a, b, o) })
	out := make([]protocol.QueueEntry, 0, len(candidates))
	for i, c := range candidates {
		t := tasks[c.TaskID]
		out = append(out, protocol.QueueEntry{
			Place: i + 1, ActionID: c.ActionID, TaskID: c.TaskID, Title: t.Title, Project: t.Project, Kind: c.Kind, Rating: c.Rating,
			WaitedMillis: max(o.Now-c.CreatedAt, 0), Score: scheduler.Score(c, o),
		})
	}
	return out, nil
}

// candidatesOf turns the claim candidates read from the store into the
// facts the scheduler rules take.
func candidatesOf(rows []store.CandidateRow) ([]scheduler.Candidate, error) {
	out := make([]scheduler.Candidate, 0, len(rows))
	for _, r := range rows {
		var spec domain.Spec
		if err := json.Unmarshal([]byte(r.SpecJSON), &spec); err != nil {
			return nil, fmt.Errorf("app: decode spec of %s: %w", r.Action.TaskID, err)
		}
		out = append(out, scheduler.Candidate{
			ActionID: r.Action.ActionID, TaskID: r.Action.TaskID, Kind: r.Action.Kind, Status: r.Action.Status,
			Generation: r.Action.Generation, TargetSpecVersion: r.Action.TargetSpecVersion,
			TargetArtifactID: r.Action.TargetArtifactID, CreatedAt: r.Action.CreatedAt,
			Lifecycle: r.Lifecycle, Control: r.Control, Block: r.Block, SpecVersion: r.SpecVersion,
			ImplementGeneration: r.ImplementGeneration, CurrentArtifactID: r.CurrentArtifactID, Rating: r.Rating, Weight: r.Weight,
			DependenciesDone: r.DependenciesDone, Requirements: spec.Requirements, Repos: spec.Workspace.Repos(),
			WorkspaceHost: r.WorkspaceHost, WorkspaceOccupied: r.WorkspaceOccupied,
		})
	}
	return out, nil
}
