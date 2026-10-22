package app

import (
	"context"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ReleaseWorkspace records the runner's proof that the task no longer has local writers.
type ReleaseWorkspace struct{}

func (ReleaseWorkspace) Operation() string { return protocol.OpWorkspaceRelease }
func (ReleaseWorkspace) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := requireActiveHost(ctx, x, q)
	return err
}
func (ReleaseWorkspace) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.WorkspaceRelease
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := store.GetWorkspace(ctx, tx.Q(), p.WorkspaceID)
	if err != nil {
		return Outcome{}, err
	}
	if w.HostID != x.Principal.Subject {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "workspace belongs to another host")
	}
	var known bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_runs wr JOIN runs r ON r.run_id=wr.run_id WHERE wr.workspace_id=? AND r.task_id=?)`, w.WorkspaceID, p.TaskID).Scan(&known); err != nil {
		return Outcome{}, fmt.Errorf("app: verify workspace release: %w", err)
	}
	if !known {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "task has no run in this workspace")
	}
	if w.OccupiedBy != p.TaskID {
		return Outcome{Result: protocol.WorkspaceReleaseResult{Released: true}, NoEffect: true}, nil
	}
	var blocked bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT
	 EXISTS(SELECT 1 FROM task_workspace_bindings b JOIN tasks t ON t.task_id=b.task_id WHERE b.workspace_id=? AND b.task_id=? AND t.lifecycle NOT IN ('completed','cancelled'))
	 OR EXISTS(SELECT 1 FROM workspace_runs wr JOIN runs r ON r.run_id=wr.run_id WHERE wr.workspace_id=? AND r.observed_state IN ('starting','running','waiting','interrupted','unknown'))`, w.WorkspaceID, p.TaskID, w.WorkspaceID).Scan(&blocked); err != nil {
		return Outcome{}, fmt.Errorf("app: check workspace writers: %w", err)
	}
	if blocked {
		return Outcome{Result: protocol.WorkspaceReleaseResult{}, NoEffect: true}, nil
	}
	now := x.Now.UnixMilli()
	if _, err := tx.Exec(ctx, `UPDATE workspaces SET occupied_by=NULL,status='retained',released_at=?,updated_at=? WHERE workspace_id=? AND occupied_by=?`, now, now, w.WorkspaceID, p.TaskID); err != nil {
		return Outcome{}, fmt.Errorf("app: release workspace: %w", err)
	}
	return Outcome{Result: protocol.WorkspaceReleaseResult{Released: true}, Events: []Event{{ObjectType: "workspace", ObjectID: w.WorkspaceID, Kind: "workspace.released", Body: map[string]any{"task_id": p.TaskID}}}}, nil
}

// ReserveWorkspaceCleanup serializes retention expiry with new reuse bindings.
type ReserveWorkspaceCleanup struct{}

func (ReserveWorkspaceCleanup) Operation() string { return protocol.OpWorkspaceCleanup }
func (ReserveWorkspaceCleanup) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := requireActiveHost(ctx, x, q)
	return err
}
func (ReserveWorkspaceCleanup) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.WorkspaceCleanupRequest
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Limit == 0 {
		p.Limit = 8
	}
	if p.Limit < 1 || p.Limit > 32 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "cleanup limit must be between 1 and 32")
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT w.workspace_id FROM workspaces w WHERE w.host_id=? AND
	 (w.status='removing' OR (w.kind='directory' AND w.status='retained' AND w.occupied_by IS NULL AND w.path<>'' AND w.released_at<=?
	 AND NOT EXISTS(SELECT 1 FROM task_workspace_bindings b JOIN tasks t ON t.task_id=b.task_id WHERE b.workspace_id=w.workspace_id AND t.lifecycle NOT IN ('completed','cancelled'))
	 AND NOT EXISTS(SELECT 1 FROM workspace_runs wr JOIN runs r ON r.run_id=wr.run_id WHERE wr.workspace_id=w.workspace_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown'))))
	 ORDER BY w.updated_at,w.workspace_id LIMIT ?`, x.Principal.Subject, x.Now.Add(-7*24*time.Hour).UnixMilli(), p.Limit)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: list expired workspaces: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Outcome{}, fmt.Errorf("app: scan expired workspace: %w", err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Outcome{}, fmt.Errorf("app: list expired workspaces: %w", err)
	}
	result := protocol.WorkspaceCleanupResult{Workspaces: []protocol.WorkspaceCleanup{}}
	var events []Event
	for _, id := range ids {
		w, err := store.GetWorkspace(ctx, tx.Q(), id)
		if err != nil {
			return Outcome{}, err
		}
		kind := "workspace.cleanup_retried"
		if w.CleanupID == "" {
			kind = "workspace.cleanup_reserved"
			w.CleanupID = x.IDs.New(protocol.PrefixWorkspaceCleanup)
		}
		// Rotate already-reserved work as well, so a blocked provider resource
		// cannot monopolize every later bounded cleanup batch.
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET status='removing',cleanup_id=?,updated_at=MAX(updated_at+1,?) WHERE workspace_id=?`, w.CleanupID, x.Now.UnixMilli(), id); err != nil {
			return Outcome{}, fmt.Errorf("app: reserve workspace cleanup: %w", err)
		}
		events = append(events, Event{ObjectType: "workspace", ObjectID: id, Kind: kind, Body: map[string]any{"cleanup_id": w.CleanupID}})
		result.Workspaces = append(result.Workspaces, protocol.WorkspaceCleanup{CleanupID: w.CleanupID, WorkspaceID: id, Kind: w.Kind, Name: w.Name, Path: w.Path, ProviderID: w.ProviderID, ProviderInstanceID: w.ProviderInstanceID})
	}
	return Outcome{Result: result, Events: events, NoEffect: len(events) == 0}, nil
}

// CompleteWorkspaceCleanup records removal only for the exact reservation and host.
type CompleteWorkspaceCleanup struct{}

func (CompleteWorkspaceCleanup) Operation() string { return protocol.OpWorkspaceCleanupDone }
func (CompleteWorkspaceCleanup) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := requireActiveHost(ctx, x, q)
	return err
}
func (CompleteWorkspaceCleanup) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.WorkspaceCleanupDone
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := store.GetWorkspace(ctx, tx.Q(), p.WorkspaceID)
	if err != nil {
		return Outcome{}, err
	}
	if w.HostID != x.Principal.Subject {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "workspace belongs to another host")
	}
	if p.CleanupID == "" || p.CleanupID != w.CleanupID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "cleanup reservation does not match")
	}
	if w.Status == store.WorkspaceRemoved {
		return Outcome{Result: map[string]bool{"removed": true}, NoEffect: true}, nil
	}
	if w.Status != store.WorkspaceRemoving {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "workspace is not reserved for cleanup")
	}
	if err := tx.SetWorkspaceStatus(ctx, w.WorkspaceID, store.WorkspaceRemoved, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]bool{"removed": true}, Events: []Event{{ObjectType: "workspace", ObjectID: w.WorkspaceID, Kind: "workspace.removed", Body: map[string]any{"cleanup_id": p.CleanupID}}}}, nil
}
