package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// expandTemplate replaces a template reference without mounts by the
// template's mounts and profile, keeping the template name as provenance.
func expandTemplate(ctx context.Context, q store.Querier, w domain.Workspace) (domain.Workspace, error) {
	if w.Template == "" {
		return w, nil
	}
	if len(w.Mounts) > 0 || w.Profile != "" {
		return domain.Workspace{}, protocol.Errorf(protocol.CodeInvalidRequest, "give either a workspace template or explicit mounts, not both")
	}
	body, err := store.GetTemplate(ctx, q, w.Template)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Workspace{}, protocol.Errorf(protocol.CodeInvalidRequest, "workspace template %q does not exist", w.Template)
	}
	if err != nil {
		return domain.Workspace{}, err
	}
	var t domain.Workspace
	if err := json.Unmarshal([]byte(body), &t); err != nil {
		return domain.Workspace{}, fmt.Errorf("app: decode template %s: %w", w.Template, err)
	}
	t.Template = w.Template
	return t, nil
}

// checkRepos requires every repository to be registered and not retired.
func checkRepos(ctx context.Context, q store.Querier, repos []string) error {
	for _, name := range repos {
		r, err := store.GetRepo(ctx, q, name)
		if errors.Is(err, store.ErrNotFound) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "repo %q is not registered", name)
		}
		if err != nil {
			return err
		}
		if r.Retired {
			return protocol.Errorf(protocol.CodeInvalidRequest, "repo %q is retired", name)
		}
	}
	return nil
}

// addedRepos returns the repositories of next that current does not mount.
// Repositories a task already uses stay usable after they are retired.
func addedRepos(current, next domain.Workspace) []string {
	var out []string
	for _, r := range next.Repos() {
		if !slices.Contains(current.Repos(), r) {
			out = append(out, r)
		}
	}
	return out
}

// reserveWorkspaceName validates the requested workspace name, or derives
// one from the task ID, and checks that no other task holds it.
func reserveWorkspaceName(ctx context.Context, q store.Querier, name, taskID string) (string, error) {
	if name == "" {
		name = "task-" + strings.ToLower(taskID[len(taskID)-8:])
	}
	if !domain.ValidWorkspaceName(name) {
		return "", protocol.Errorf(protocol.CodeInvalidRequest,
			"workspace name %q must be a single lowercase directory name that is also valid in a git branch", name)
	}
	taken, err := store.WorkspaceNameTaken(ctx, q, name)
	if err != nil {
		return "", err
	}
	if taken {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "workspace name %q is already used by another task", name)
	}
	return name, nil
}
