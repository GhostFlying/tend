package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
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
	if err := applyWorkItemDependencies(ctx, q, candidates); err != nil {
		return nil, err
	}
	tasks := make(map[string]store.CandidateRow, len(rows))
	for _, r := range rows {
		tasks[r.Action.TaskID] = r
	}
	hosts, err := schedulingHosts(ctx, q)
	if err != nil {
		return nil, err
	}
	candidates = slices.DeleteFunc(candidates, func(c scheduler.Candidate) bool { return scheduler.Held(c) != "" })
	slices.SortFunc(candidates, func(a, b scheduler.Candidate) int { return scheduler.Compare(a, b, o) })
	out := make([]protocol.QueueEntry, 0, len(candidates))
	for i, c := range candidates {
		t := tasks[c.TaskID]
		out = append(out, protocol.QueueEntry{
			Place: i + 1, ActionID: c.ActionID, TaskID: c.TaskID, Title: t.Title, Project: t.Project, Kind: c.Kind, Rating: c.Rating,
			WaitedMillis: max(o.Now-c.CreatedAt, 0), Score: scheduler.Score(c, o), Unplaceable: unplaceable(hosts, c),
		})
	}
	return out, nil
}

// schedulingHosts returns the active hosts as the scheduler sees them,
// each with every slot free.
func schedulingHosts(ctx context.Context, q store.Querier) ([]scheduler.Host, error) {
	rows, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.Host, 0, len(rows))
	for _, h := range rows {
		runtime, err := store.GetHostRuntime(ctx, q, h.HostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		out = append(out, scheduler.Host{
			ID: h.HostID, OS: h.OS, Active: true, Labels: h.Labels, Repos: h.Repos, Slots: h.Slots,
			Worker: h.Worker.Capabilities(), WorkspaceProtocol: h.WorkspaceProtocol, TaskInputs: runtimeSupportsTaskInputs(&runtime.Runtime), WorkerObservation: runtimeSupportsObservation(&runtime.Runtime), AcceptanceExecution: slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAcceptanceExecution),
		})
	}
	return out, nil
}

// unplaceable says why none of hosts would take c even with a free slot,
// or "" when one would.
func unplaceable(hosts []scheduler.Host, c scheduler.Candidate) string {
	if len(hosts) == 0 {
		return "no host is active"
	}
	var why []string
	for _, h := range hosts {
		reason := scheduler.Skip(h, c)
		if reason == "" {
			return ""
		}
		if !slices.Contains(why, reason) {
			why = append(why, reason)
		}
	}
	return strings.Join(why, "; ")
}

// candidatesOf turns the claim candidates read from the store into the
// facts the scheduler rules take.
func candidatesOf(rows []store.CandidateRow) ([]scheduler.Candidate, error) {
	return views.SchedulingCandidates(rows)
}
