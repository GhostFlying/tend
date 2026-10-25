package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

type executionFacts struct {
	candidates map[string]scheduler.Candidate
	hosts      []executionHost
}

type executionHost struct {
	scheduler.Host
	available bool
}

// SummaryWithExecution reads the execution facts accompanying one task summary.
func SummaryWithExecution(ctx context.Context, q store.Querier, row store.TaskRow) (protocol.TaskSummary, error) {
	summaries, err := SummariesWithExecution(ctx, q, []store.TaskRow{row})
	if err != nil {
		return protocol.TaskSummary{}, err
	}
	return summaries[0], nil
}

// SummariesWithExecution shares scheduling facts across a task listing.
func SummariesWithExecution(ctx context.Context, q store.Querier, rows []store.TaskRow) ([]protocol.TaskSummary, error) {
	out := make([]protocol.TaskSummary, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return nil, err
	}
	return facts.summaries(ctx, q, rows)
}

func (facts executionFacts) summaries(ctx context.Context, q store.Querier, rows []store.TaskRow) ([]protocol.TaskSummary, error) {
	out := make([]protocol.TaskSummary, 0, len(rows))
	var err error
	for _, row := range rows {
		summary := Summary(row)
		summary.PrimaryWorkItemID, summary.PrimaryWorkItemTitle, err = store.TaskWorkItemLabel(ctx, q, row.TaskID)
		if err != nil {
			return nil, err
		}
		summary.Execution, err = facts.task(ctx, q, row)
		if err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, nil
}

func readExecutionFacts(ctx context.Context, q store.Querier) (executionFacts, error) {
	facts := executionFacts{candidates: map[string]scheduler.Candidate{}}
	rows, err := store.ListClaimCandidates(ctx, q)
	if err != nil {
		return facts, err
	}
	candidates, err := SchedulingCandidates(rows)
	if err != nil {
		return facts, err
	}
	if err := ApplyWorkItemDependencies(ctx, q, candidates); err != nil {
		return facts, err
	}
	for _, c := range candidates {
		if c.TargetSpecVersion != c.SpecVersion || c.Kind == protocol.ActionImplement && c.Generation != c.ImplementGeneration || c.Kind == protocol.ActionAccept && (c.Lifecycle != protocol.TaskAwaitingAcceptance || c.TargetArtifactID != c.CurrentArtifactID) {
			continue
		}
		facts.candidates[c.TaskID] = c
	}
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return facts, err
	}
	for _, h := range hosts {
		busy, err := store.CountOpenRuns(ctx, q, h.HostID)
		if err != nil {
			return facts, err
		}
		runtime, err := store.GetHostRuntime(ctx, q, h.HostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return facts, err
		}
		hasRuntime := err == nil
		control, err := store.GetHostControl(ctx, q, h.HostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return facts, err
		}
		available := control.Mode == protocol.RunnerNormal
		if hasRuntime {
			available = available && runtime.Online && runtime.Runtime.Mode == protocol.RunnerNormal && runtime.Runtime.AppliedControlRevision == control.Revision
		}
		facts.hosts = append(facts.hosts, executionHost{Host: scheduler.Host{
			ID: h.HostID, OS: h.OS, Active: true, Labels: h.Labels, Repos: h.Repos, Slots: h.Slots, Busy: busy,
			Worker: h.Worker.Capabilities(), WorkspaceProtocol: h.WorkspaceProtocol,
			TaskInputs: slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityTaskInputs),
		}, available: available})
	}
	return facts, nil
}

func (f executionFacts) task(ctx context.Context, q store.Querier, t store.TaskRow) (*protocol.TaskExecutionView, error) {
	v := &protocol.TaskExecutionView{State: protocol.ExecutionIdle}
	switch t.Lifecycle {
	case protocol.TaskCompleted:
		v.State = protocol.ExecutionCompleted
		return v, nil
	case protocol.TaskCancelled:
		v.State = protocol.ExecutionCancelled
		return v, nil
	}
	ownerBlocked := t.BlockReason == protocol.BlockWaitingDecision || t.BlockReason == protocol.BlockWaitingApproval ||
		t.BlockReason == protocol.BlockBudgetExhausted || t.BlockReason == protocol.BlockAcceptanceEnvUnavailable || t.BlockReason == protocol.BlockWorkspaceUnavailable
	runs, err := store.ListRunsByTask(ctx, q, t.TaskID)
	if err != nil {
		return nil, err
	}
	// A superseded action revokes authorization, not the physical possibility
	// that its old worker still holds the workspace. Preserve unresolved facts.
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].ObservedState == protocol.RunUnknown {
			v.State = protocol.ExecutionUnknown
			v.RunID = runs[i].RunID
			v.Role = runs[i].Role
			return v, nil
		}
	}
	actions, err := store.ListActions(ctx, q, t.TaskID)
	if err != nil {
		return nil, err
	}
	current := map[string]bool{}
	for _, action := range actions {
		current[action.ActionID] = action.TargetSpecVersion == t.SpecVersion && action.Status != protocol.ActionCancelled && action.Status != protocol.ActionSuperseded
	}
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.ObservedState == protocol.RunFinished || run.ObservedState == protocol.RunFailed {
			continue
		}
		v.RunID = run.RunID
		v.Role = run.Role
		// Uncertainty about a worker takes precedence over its last known wait.
		// Questions from unrelated or historical runs are not execution blockers.
		if run.ObservedState != protocol.RunUnknown && run.ObservedState != protocol.RunInterrupted {
			waiting := ownerBlocked && current[run.ActionID]
			if !waiting && current[run.ActionID] && run.DesiredState != protocol.DesiredStopped {
				questions, err := store.ListWorkerQuestions(ctx, q, run.RunID)
				if err != nil {
					return nil, err
				}
				for _, question := range questions {
					if question.Status == store.DecisionOpen && question.ActionID == run.ActionID && question.SpecVersion == t.SpecVersion {
						waiting = true
						break
					}
				}
			}
			if waiting {
				v.State = protocol.ExecutionWaitingOwner
				return v, nil
			}
		}
		switch run.ObservedState {
		case protocol.RunUnknown:
			v.State = protocol.ExecutionUnknown
		case protocol.RunInterrupted:
			v.State = protocol.ExecutionPaused
		case protocol.RunStarting:
			v.State = protocol.ExecutionStarting
		default:
			v.State = protocol.ExecutionRunning
			if run.Role == protocol.RoleAccept {
				v.State = protocol.ExecutionAccepting
			}
		}
		if v.State == protocol.ExecutionRunning || v.State == protocol.ExecutionAccepting || v.State == protocol.ExecutionStarting {
			v.Recovery, err = executionRecovery(ctx, q, run.RunID)
			if err != nil {
				return nil, err
			}
			if v.Recovery != nil {
				v.State = protocol.ExecutionRecovering
			}
		}
		return v, nil
	}
	// Control is an intent, not confirmation that an allocated worker stopped.
	// Without an open run it explains why this task cannot start new execution.
	if t.ControlMode != protocol.ControlManaged {
		v.State = protocol.ExecutionPaused
		return v, nil
	}
	if ownerBlocked {
		v.State = protocol.ExecutionWaitingOwner
		return v, nil
	}
	c, pending := f.candidates[t.TaskID]
	if !pending {
		return v, nil
	}
	v.State = protocol.ExecutionQueued
	v.Role = protocol.RoleImplement
	if c.Kind == protocol.ActionAccept {
		v.Role = protocol.RoleAccept
	}
	v.QueueReason = f.waitReason(c)
	if v.QueueReason.Code == protocol.QueueWorkspaceBusy {
		w, err := store.GetWorkspaceByTask(ctx, q, t.TaskID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			v.QueueReason.ResourceID = w.WorkspaceID
			v.QueueReason.HostID = w.HostID
			v.QueueReason.TaskID = w.OccupiedBy
			if v.QueueReason.TaskID == "" {
				v.QueueReason.TaskID, err = store.WorkspaceOpenRunTask(ctx, q, w.WorkspaceID)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if v.QueueReason.Code == protocol.QueueDependency {
		v.QueueReason.TaskID, err = store.FirstIncompleteDependency(ctx, q, t.TaskID)
		if err != nil {
			return nil, err
		}
		if v.QueueReason.TaskID == "" {
			v.QueueReason.TaskID = c.DependencyTaskID
		}
	}
	return v, nil
}

func (f executionFacts) waitReason(c scheduler.Candidate) *protocol.QueueWaitReason {
	if reason := scheduler.HeldReason(c); reason != nil {
		if c.Block != protocol.BlockNone {
			reason.Message += ": " + string(c.Block)
		}
		return reason
	}
	// Check host compatibility with a free slot before capacity: a full but
	// incompatible host is not evidence that freeing a slot can run this task.
	var capacity, unavailable, mismatch *protocol.QueueWaitReason
	for _, host := range f.hosts {
		free := host.Host
		free.Busy = 0
		free.Slots = max(free.Slots, 1)
		if reason := scheduler.SkipReason(free, c); reason != nil {
			mismatch = reason
			continue
		}
		if !host.available {
			unavailable = &protocol.QueueWaitReason{Code: protocol.QueueHostUnavailable, Message: "runner is offline or in maintenance", HostID: host.ID}
			continue
		}
		if reason := scheduler.SkipReason(host.Host, c); reason != nil {
			reason.HostID = host.ID
			capacity = reason
			continue
		}
		return &protocol.QueueWaitReason{Code: protocol.QueueReady, Message: "waiting for a runner to claim the queued action"}
	}
	if capacity != nil {
		return capacity
	}
	if unavailable != nil {
		return unavailable
	}
	if c.WorkspaceHost != "" {
		found := false
		for _, host := range f.hosts {
			if host.ID == c.WorkspaceHost {
				found = true
			}
		}
		if !found {
			return &protocol.QueueWaitReason{Code: protocol.QueueHostUnavailable, Message: "workspace host is not active", HostID: c.WorkspaceHost}
		}
	}
	if mismatch != nil {
		return mismatch
	}
	return &protocol.QueueWaitReason{Code: protocol.QueueHostUnavailable, Message: "no host is active"}
}

func countExecution(counts *protocol.ExecutionCounts, execution *protocol.TaskExecutionView) {
	if execution == nil {
		counts.Idle++
		return
	}
	switch execution.State {
	case protocol.ExecutionQueued:
		counts.Queued++
	case protocol.ExecutionRecovering:
		counts.Recovering++
	case protocol.ExecutionRunning:
		counts.Running++
	case protocol.ExecutionAccepting:
		counts.Accepting++
	case protocol.ExecutionStarting:
		if execution.Role == protocol.RoleAccept {
			counts.Accepting++
		} else {
			counts.Running++
		}
	case protocol.ExecutionWaitingOwner:
		counts.WaitingOwner++
	case protocol.ExecutionPaused:
		counts.Paused++
	case protocol.ExecutionUnknown:
		counts.Unknown++
	case protocol.ExecutionCompleted:
		counts.Completed++
	case protocol.ExecutionCancelled:
		counts.Cancelled++
	default:
		counts.Idle++
	}
}

// Latest durable root-turn fact wins only for the selected live run.
func executionRecovery(ctx context.Context, q store.Querier, runID string) (*protocol.ExecutionRecovery, error) {
	var kind, body string
	err := q.QueryRowContext(ctx, `SELECT kind,body_json FROM run_logs WHERE run_id=? AND kind IN ('worker.turn_failed','worker.recovery_pending','worker.recovery_started','worker.recovery_completed','worker.recovery_failed','worker.recovery_handed_back') ORDER BY seq DESC LIMIT 1`, runID).Scan(&kind, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if kind == "worker.recovery_completed" || kind == "worker.recovery_handed_back" {
		return nil, nil
	}
	var recovery protocol.ExecutionRecovery
	if err := json.Unmarshal([]byte(body), &recovery); err != nil {
		// Run logs are arbitrary JSON facts; malformed optional metadata must not break task reads.
		recovery = protocol.ExecutionRecovery{}
	}
	switch kind {
	case "worker.turn_failed":
		recovery.Phase = "turn_failed"
	case "worker.recovery_pending":
		recovery.Phase = "pending"
	case "worker.recovery_started":
		recovery.Phase = "running"
	case "worker.recovery_failed":
		recovery.Phase = "failed"
	}
	return &recovery, nil
}
