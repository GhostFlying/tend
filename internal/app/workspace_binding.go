package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func prepareWorkspace(ctx context.Context, q store.Querier, w domain.Workspace, projectTemplate, taskID string) (domain.Workspace, *store.WorkspaceRow, error) {
	switch w.ModeValue() {
	case domain.WorkspaceTemporary:
		return w, nil, w.Validate()
	case domain.WorkspaceReuse:
		if w.SourceTaskID == "" || w.SourceTaskID == taskID {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "reuse requires another source_task_id")
		}
		if w.Template != "" {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "reuse inherits its workspace; do not give a template")
		}
		// An existing binding remains pinned even if its source task starts over.
		resource, err := store.GetWorkspaceByTask(ctx, q, taskID)
		if errors.Is(err, store.ErrNotFound) {
			resource, err = store.GetWorkspaceByTask(ctx, q, w.SourceTaskID)
		}
		if errors.Is(err, store.ErrNotFound) {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "source task has no managed workspace")
		}
		if err != nil {
			return w, nil, err
		}
		if resource.Status == store.WorkspaceRemoving || resource.Status == store.WorkspaceRemoved {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "source workspace has been removed or is being cleaned up")
		}
		specTaskID := w.SourceTaskID
		if resource.TaskID == taskID {
			specTaskID = taskID
		}
		source, err := store.GetTask(ctx, q, specTaskID)
		if err != nil {
			return w, nil, err
		}
		body, err := store.GetSpec(ctx, q, source.TaskID, source.SpecVersion)
		if err != nil {
			return w, nil, err
		}
		var spec domain.Spec
		if err = json.Unmarshal([]byte(body), &spec); err != nil {
			return w, nil, fmt.Errorf("app: decode source workspace spec: %w", err)
		}
		if len(w.Mounts) > 0 && !(domain.Workspace{Mounts: w.Normalize().Mounts, Profile: spec.Workspace.Profile}).Equal(domain.Workspace{Mounts: spec.Workspace.Normalize().Mounts, Profile: spec.Workspace.Profile}) {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "reuse cannot change the source workspace mounts")
		}
		if w.Profile != "" && w.Profile != spec.Workspace.Profile {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "reuse cannot change the workspace profile")
		}
		w.Mounts, w.Profile = spec.Workspace.Mounts, spec.Workspace.Profile
		return w.Normalize(), &resource, nil
	default:
		if w.Template == "" && len(w.Mounts) == 0 && w.Profile == "" {
			w.Template = projectTemplate
		}
		expanded, err := expandTemplate(ctx, q, w)
		return expanded, nil, err
	}
}

func checkWorkspaceProtocol(ctx context.Context, q store.Querier, s domain.Spec) error {
	if s.Workspace.ModeValue() == domain.WorkspaceNew && len(s.Deliverables) == 0 {
		return nil
	}
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.WorkspaceProtocol >= 1 {
			return nil
		}
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, "no active runner supports workspace resources and composable deliverables; upgrade a runner first")
}

func workspaceMode(c scheduler.Candidate) string {
	if c.WorkspaceMode == "" {
		return domain.WorkspaceNew
	}
	return c.WorkspaceMode
}
func workspaceKind(c scheduler.Candidate) string {
	if workspaceMode(c) == domain.WorkspaceTemporary {
		return "directory"
	}
	return "git"
}

func shortTaskID(ctx context.Context, q store.Querier, id string) (string, error) {
	for n := min(8, len(id)); n < len(id); n++ {
		short := id[len(id)-n:]
		var count int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE substr(task_id,?)=?`, -n, short).Scan(&count); err != nil {
			return "", fmt.Errorf("app: resolve short task ID: %w", err)
		}
		if count == 1 {
			return short, nil
		}
	}
	return id, nil
}
