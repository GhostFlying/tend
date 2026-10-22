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

func (r *Runner) reconcileWorkspaceProcesses(ctx context.Context) (map[string]bool, error) {
	env, err := r.newEnvelope(protocol.OpWorkspaceReconcile, protocol.Target{Type: "host", ID: r.cfg.HostID}, struct{}{})
	if err != nil {
		return nil, err
	}
	op, err := r.hostOperation(ctx, env)
	if err != nil {
		return nil, err
	}
	var result protocol.WorkspaceReconcileResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	for _, resource := range result.Workspaces {
		if err := r.reconcileWorkspace(ctx, resource); err != nil {
			blocked[resource.WorkspaceID] = true
			r.cfg.Log.WarnContext(ctx, "workspace needs process reconciliation; retained for its owner", "workspace_id", resource.WorkspaceID, "task_id", resource.TaskID, "err", err)
		}
	}
	return blocked, nil
}

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
			return err
		}
		if origin == protocol.WorkerAppServer {
			if run.Assignment.Workspace.ProviderID != "" || run.Assignment.Workspace.ProviderInstanceID != "" {
				return errors.New("historical app-server origin conflicts with an external provider")
			}
			if !slices.Contains(p.FinishedRunIDs, id) {
				return errors.New("historical app-server session closure lacks a successful final report")
			}
			if err := traex.ReconcileCompletedSession(ctx, run.Thread, run.Held, run.UpdatedAtMillis); err != nil {
				return err
			}
		} else {
			if origin != protocol.WorkerOrca || r.cfg.Worker != protocol.WorkerOrca {
				return errors.New("historical process proof requires its original harness")
			}
			rel, ok := r.cfg.Workers.(harness.HistoricalReleaser)
			if !ok {
				return errors.New("historical process proof is unavailable for this harness")
			}
			if err := rel.ReleaseHistorical(ctx, id, run.Thread, run.Held, run.UpdatedAtMillis); err != nil {
				return err
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
