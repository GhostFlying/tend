package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
		if row.Lifecycle.Terminal() {
			if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_at),0) FROM events WHERE object_type='task' AND object_id=? AND kind=?`, row.TaskID, "task."+string(row.Lifecycle)).Scan(&summary.EndedAt); err != nil {
				return nil, fmt.Errorf("views: read task terminal time: %w", err)
			}
		}
		summary.PrimaryWorkItemID, summary.PrimaryWorkItemTitle, err = store.TaskIssueLabel(ctx, q, row.TaskID)
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
	if err := ApplyIssueDependencies(ctx, q, candidates); err != nil {
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
			TaskInputs:          slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityTaskInputs),
			AcceptanceExecution: slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAcceptanceExecution),
			WorkerObservation:   slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityWorkerObservation),
		}, available: available})
	}
	return facts, nil
}

func (f executionFacts) task(ctx context.Context, q store.Querier, t store.TaskRow) (*protocol.TaskExecutionView, error) {
	v := &protocol.TaskExecutionView{State: protocol.ExecutionIdle}
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
			v.Startup, err = executionStartup(ctx, q, runs[i].RunID)
			if err != nil {
				return nil, err
			}
			return v, nil
		}
	}
	actions, err := store.ListActions(ctx, q, t.TaskID)
	if err != nil {
		return nil, err
	}
	current := map[string]bool{}
	latestExecutionAction := ""
	for _, action := range actions {
		current[action.ActionID] = action.TargetSpecVersion == t.SpecVersion && action.Status != protocol.ActionCancelled && action.Status != protocol.ActionSuperseded
		if current[action.ActionID] && (action.Kind == protocol.ActionImplement || action.Kind == protocol.ActionAccept) {
			latestExecutionAction = action.ActionID
		}
	}
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.ObservedState == protocol.RunFinished || run.ObservedState == protocol.RunFailed {
			continue
		}
		v.RunID = run.RunID
		v.Role = run.Role
		v.Startup, err = executionStartup(ctx, q, run.RunID)
		if err != nil {
			return nil, err
		}
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
		// Recovery is no longer authorized after the task's logical outcome, but
		// the underlying worker remains visible until its stop is confirmed.
		if !t.Lifecycle.Terminal() && (v.State == protocol.ExecutionRunning || v.State == protocol.ExecutionAccepting || v.State == protocol.ExecutionStarting) {
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
	// A terminal lifecycle is the logical outcome, not proof that an allocated
	// worker stopped. Only use it after every run has physically settled.
	switch t.Lifecycle {
	case protocol.TaskCompleted:
		v.State = protocol.ExecutionCompleted
		return v, nil
	case protocol.TaskCancelled:
		v.State = protocol.ExecutionCancelled
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
		// The failed run explains its unresolved owner decision, but must not
		// override a later action's scheduling state after retry or spec revision.
		if len(runs) > 0 && runs[len(runs)-1].ActionID == latestExecutionAction {
			v.Startup, err = executionStartup(ctx, q, runs[len(runs)-1].RunID)
			if err != nil {
				return nil, err
			}
		}
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
		counts.Starting++
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
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT seq,kind,body_json FROM run_logs WHERE run_id=? AND kind IN ('worker.root_background_pending','worker.root_observation_unknown','worker.turn_failed','worker.recovery_pending','worker.recovery_started','worker.recovery_completed','worker.recovery_failed','worker.recovery_handed_back','worker.startup_failed','worker.startup_retry_pending','worker.startup_started') AND (kind NOT IN ('worker.root_observation_unknown','worker.root_background_pending') OR seq > COALESCE((SELECT MAX(seq) FROM run_logs WHERE run_id=? AND kind='worker.root_observation_resolved'),0)) ORDER BY seq DESC LIMIT 1`, runID, runID).Scan(&seq, &kind, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if kind == "worker.recovery_completed" || kind == "worker.recovery_handed_back" || kind == "worker.startup_started" {
		return nil, nil
	}
	var recovery protocol.ExecutionRecovery
	if err := json.Unmarshal([]byte(body), &recovery); err != nil {
		// Run logs are arbitrary JSON facts; malformed optional metadata must not break task reads.
		recovery = protocol.ExecutionRecovery{}
	}
	switch kind {
	case "worker.startup_failed":
		recovery.Phase = "startup_failed"
	case "worker.startup_retry_pending":
		recovery.Phase = "startup_retry_pending"
	case "worker.root_background_pending":
		recovery.Phase = "background_pending"
	case "worker.root_observation_unknown":
		recovery.Phase = "observation_unknown"
	case "worker.turn_failed":
		recovery.Phase = "turn_failed"
		recovery.RootState = "failed"
		var native struct {
			RootTurn *struct {
				Operations string `json:"operations"`
				ErrorCode  string `json:"errorCode"`
			} `json:"root_turn"`
		}
		if json.Unmarshal([]byte(body), &native) == nil && native.RootTurn != nil {
			if recovery.Code == "" {
				recovery.Code = native.RootTurn.ErrorCode
			}
			if native.RootTurn.Operations == "unknown" {
				recovery.Phase = "observation_unknown"
				recovery.BackgroundPending = true
			} else if native.RootTurn.Operations == "pending" {
				recovery.Phase = "background_pending"
				recovery.BackgroundPending = true
			}
		}
		var observedSeq int64
		var observedBody string
		err := q.QueryRowContext(ctx, `SELECT seq,body_json FROM run_logs WHERE run_id=? AND kind='worker.root_observation_resolved' ORDER BY seq DESC LIMIT 1`, runID).Scan(&observedSeq, &observedBody)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		var observed protocol.ExecutionRecovery
		if err == nil && observedSeq > seq && json.Unmarshal([]byte(observedBody), &observed) == nil && observed.RootState == "failed" {
			recovery.Phase = "turn_failed"
			recovery.BackgroundPending = false
			if observed.Code != "" {
				recovery.Code = observed.Code
			}
		}
	case "worker.recovery_pending":
		recovery.Phase = "pending"
	case "worker.recovery_started":
		recovery.Phase = "running"
	case "worker.recovery_failed":
		recovery.Phase = "failed"
	}
	return &recovery, nil
}

func executionStartup(ctx context.Context, q store.Querier, runID string) (*protocol.ExecutionStartup, error) {
	var kind, body string
	err := q.QueryRowContext(ctx, `SELECT kind,body_json FROM run_logs WHERE run_id=? AND kind IN ('worker.startup_failed','worker.startup_retry_pending','worker.startup_started') ORDER BY seq DESC LIMIT 1`, runID).Scan(&kind, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fact struct {
		Attempts   int `json:"attempts"`
		Diagnostic struct {
			Code             string `json:"code"`
			PromptWriteState string `json:"promptWriteState"`
			ElapsedMS        int64  `json:"elapsedMs"`
		} `json:"diagnostic"`
	}
	if json.Unmarshal([]byte(body), &fact) != nil {
		return nil, nil
	}
	return &protocol.ExecutionStartup{Phase: kind[len("worker.startup_"):], Code: fact.Diagnostic.Code, PromptWriteState: fact.Diagnostic.PromptWriteState, ElapsedMS: fact.Diagnostic.ElapsedMS, Attempts: fact.Attempts}, nil
}
