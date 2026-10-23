package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// workspaceProcessReconciliation keeps resource quarantine independent of
// readiness: retaining an untouched legacy resource need not block replacement
// of an otherwise drained runner.
type workspaceProcessReconciliation struct {
	blocked            map[string]bool
	maintenanceBlocked bool
}

func (r *Runner) reconcileWorkspaceProcesses(ctx context.Context) (workspaceProcessReconciliation, error) {
	env, err := r.newEnvelope(protocol.OpWorkspaceReconcile, protocol.Target{Type: "host", ID: r.cfg.HostID}, struct{}{})
	if err != nil {
		return workspaceProcessReconciliation{}, err
	}
	op, err := r.hostOperation(ctx, env)
	if err != nil {
		return workspaceProcessReconciliation{}, err
	}
	var result protocol.WorkspaceReconcileResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return workspaceProcessReconciliation{}, err
	}
	resultState := workspaceProcessReconciliation{blocked: map[string]bool{}}
	for _, resource := range result.Workspaces {
		legacy, legacyErr := r.legacyResourceWarning(ctx, resource)
		if legacyErr != nil {
			return resultState, legacyErr
		}
		if legacy {
			resultState.blocked[resource.WorkspaceID] = true
			r.cfg.Log.WarnContext(ctx, "legacy workspace retained without blocking runner maintenance", "workspace_id", resource.WorkspaceID, "task_id", resource.TaskID)
			continue
		}
		if err := r.reconcileWorkspace(ctx, resource); err != nil {
			resultState.blocked[resource.WorkspaceID] = true
			quarantined := false
			if errors.Is(err, errWorkspaceProcessUnconfirmed) {
				var classificationErr error
				quarantined, classificationErr = r.terminalResourceQuarantine(ctx, resource)
				if classificationErr != nil {
					return resultState, classificationErr
				}
			}
			if !quarantined {
				resultState.maintenanceBlocked = true
			}
			r.cfg.Log.WarnContext(ctx, "workspace needs process reconciliation; retained for its owner", "workspace_id", resource.WorkspaceID, "task_id", resource.TaskID, "maintenance_blocked", !quarantined, "err", err)
		}
	}
	return resultState, nil
}

// Native proof failures retain the resource, whereas journal failures must also
// prevent declaring the runner drained. They must not share an exception path.
var errWorkspaceProcessUnconfirmed = errors.New("workspace process stop is unconfirmed")

func (r *Runner) reconcileWorkspace(ctx context.Context, p protocol.WorkspaceReconcile) error {
	r.mu.Lock()
	busy := false
	for _, a := range r.active {
		if a.as.TaskID == p.TaskID || a.as.Workspace.ID == p.WorkspaceID {
			busy = true
			break
		}
	}
	r.mu.Unlock()
	if busy {
		return errors.New("local run is still active")
	}
	if len(p.RunIDs) == 0 {
		return errors.New("resource has no run identities to verify")
	}
	for _, id := range p.RunIDs {
		run, err := r.cfg.Journal.HistoricalRun(ctx, id)
		if err != nil {
			return fmt.Errorf("historical run %s unavailable: %w", id, err)
		}
		if run.Assignment.TaskID != p.TaskID || run.Assignment.Workspace.ID != "" && run.Assignment.Workspace.ID != p.WorkspaceID {
			return errors.New("historical run resource identity does not match")
		}
		if run.Phase != journal.PhaseDone {
			return errors.New("historical run is not locally complete")
		}
		if run.StoppedConfirmed {
			continue
		}
		if run.Thread == "" && run.Attempts == 0 {
			if err := r.cfg.Journal.ConfirmStopped(ctx, id); err != nil {
				return err
			}
			continue
		}
		origin, err := recordedWorkerOrigin(run)
		if err != nil {
			return errors.Join(errWorkspaceProcessUnconfirmed, err)
		}
		if origin == protocol.WorkerAppServer {
			if run.Assignment.Workspace.ProviderID != "" || run.Assignment.Workspace.ProviderInstanceID != "" {
				return fmt.Errorf("%w: historical app-server origin conflicts with an external provider", errWorkspaceProcessUnconfirmed)
			}
			if !slices.Contains(p.FinishedRunIDs, id) {
				return fmt.Errorf("%w: historical app-server session closure lacks a successful final report", errWorkspaceProcessUnconfirmed)
			}
			if err := traex.ReconcileCompletedSession(ctx, run.Thread, run.Held, run.UpdatedAtMillis); err != nil {
				return errors.Join(errWorkspaceProcessUnconfirmed, err)
			}
		} else {
			if origin != protocol.WorkerOrca || r.cfg.Worker != protocol.WorkerOrca {
				return fmt.Errorf("%w: historical process proof requires its original harness", errWorkspaceProcessUnconfirmed)
			}
			rel, ok := r.cfg.Workers.(harness.HistoricalReleaser)
			if !ok {
				return fmt.Errorf("%w: historical process proof is unavailable for this harness", errWorkspaceProcessUnconfirmed)
			}
			if err := rel.ReleaseHistorical(ctx, id, run.Thread, run.Held, run.UpdatedAtMillis); err != nil {
				return errors.Join(errWorkspaceProcessUnconfirmed, err)
			}
		}
		if err := r.cfg.Journal.ConfirmStopped(ctx, id); err != nil {
			return err
		}
	}
	return r.cfg.Journal.QueueWorkspaceRelease(ctx, protocol.WorkspaceRelease{WorkspaceID: p.WorkspaceID, TaskID: p.TaskID})
}

// recordedWorkerOrigin uses immutable receipt metadata, or the two known legacy
// thread encodings. The current harness never supplies missing historical origin.
func recordedWorkerOrigin(run journal.Run) (protocol.WorkerKind, error) {
	if run.WorkerKind == protocol.WorkerAppServer || run.WorkerKind == protocol.WorkerOrca {
		return run.WorkerKind, nil
	}
	if run.WorkerKind != "" {
		return "", errors.New("recorded worker origin is unsupported")
	}
	switch {
	case traex.IsLegacyThreadID(run.Thread):
		return protocol.WorkerAppServer, nil
	case strings.HasPrefix(strings.TrimSpace(run.Thread), "{") && json.Valid([]byte(run.Thread)):
		return protocol.WorkerOrca, nil
	default:
		return "", errors.New("historical worker origin is unknown")
	}
}

// legacyResourceWarning recognizes only receipts from before worker/thread and
// physical-resource metadata existed. It authorizes no resource mutation and
// does not claim that an unrecorded process stopped. The server must positively
// identify a terminal task; detached resources of live tasks do not qualify.
func (r *Runner) legacyResourceWarning(ctx context.Context, p protocol.WorkspaceReconcile) (bool, error) {
	return r.resourceQuarantine(ctx, p, true)
}

// terminalResourceQuarantine authorizes only retaining an uncertain resource.
// It never turns missing native proof into confirmation or resource release.
func (r *Runner) terminalResourceQuarantine(ctx context.Context, p protocol.WorkspaceReconcile) (bool, error) {
	return r.resourceQuarantine(ctx, p, false)
}

func (r *Runner) resourceQuarantine(ctx context.Context, p protocol.WorkspaceReconcile, legacyOnly bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !p.TaskTerminal || len(p.RunIDs) == 0 {
		return false, nil
	}
	r.mu.Lock()
	active := false
	for _, a := range r.active {
		if a.as.TaskID == p.TaskID || a.as.Workspace.ID == p.WorkspaceID {
			active = true
			break
		}
	}
	r.mu.Unlock()
	if active {
		return false, nil
	}
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return false, err
	}
	releases, err := r.cfg.Journal.WorkspaceReleases(ctx)
	if err != nil {
		return false, err
	}
	if len(requests) != 0 || len(releases) != 0 {
		return false, nil
	}
	for _, id := range p.RunIDs {
		run, err := r.cfg.Journal.HistoricalRun(ctx, id)
		if errors.Is(err, journal.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		ws := run.Assignment.Workspace
		if run.Assignment.TaskID != p.TaskID || ws.ID != "" && ws.ID != p.WorkspaceID || run.Phase != journal.PhaseDone || run.Held || run.Fenced {
			return false, nil
		}
		if legacyOnly && (run.WorkerKind != "" || run.Thread != "" || run.Assignment.Protocol != 0 || ws.ID != "" || ws.Kind != "" || ws.ProviderID != "" || ws.ProviderInstanceID != "" || ws.Path != "") {
			return false, nil
		}
	}
	return true, nil
}
