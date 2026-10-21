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

// MultiMountRefused is why a workspace with more than one mount is
// refused while no active host's worker can lay one out (D64).
const MultiMountRefused = "no active host's worker supports a workspace with more than one mount; give the task a single repository"

// checkMounts refuses a workspace with more than one mount when no active
// host's worker supports it (D64), so a task is not created only to wait
// for a host that cannot exist under the current configuration.
func checkMounts(ctx context.Context, q store.Querier, w domain.Workspace) error {
	if len(w.Mounts) <= 1 {
		return nil
	}
	only, err := SingleMountOnly(ctx, q)
	if err != nil || !only {
		return err
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, MultiMountRefused)
}

// SingleMountOnly reports whether there are active hosts and none of
// their workers lays out a workspace with more than one mount. Without
// any active host nothing is known about the workers yet, and nothing is
// refused for them.
func SingleMountOnly(ctx context.Context, q store.Querier) (bool, error) {
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return false, err
	}
	return len(hosts) > 0 && !slices.ContainsFunc(hosts, func(h store.HostRow) bool { return h.Worker.Capabilities().MultiMount }), nil
}

// addsMounts reports whether next has a mount current does not, by
// repository and path.
func addsMounts(current, next domain.Workspace) bool {
	return slices.ContainsFunc(next.Mounts, func(m domain.Mount) bool {
		return !slices.ContainsFunc(current.Mounts, func(c domain.Mount) bool { return c.Repo == m.Repo && c.Path == m.Path })
	})
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
	other, err := store.TaskByWorkspaceName(ctx, q, name)
	if errors.Is(err, store.ErrNotFound) {
		return name, nil
	}
	if err != nil {
		return "", err
	}
	// The caller may be repeating its own earlier creation; the task it
	// collides with lets it tell.
	pe := protocol.Errorf(protocol.CodeInvalidRequest, "workspace name %q is already used by task %s", name, other)
	pe.Details, _ = json.Marshal(map[string]string{"task_id": other})
	return "", pe
}
