package app

import (
	"context"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// resolveProject returns the project a task joins: the one named, or,
// when none is, the project of the calling agent session, or else the
// only active project.
func resolveProject(ctx context.Context, q store.Querier, name string, self protocol.Principal) (store.ProjectRow, error) {
	if name != "" {
		p, err := store.GetProject(ctx, q, name)
		if errors.Is(err, store.ErrNotFound) {
			return store.ProjectRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "project %q does not exist", name)
		}
		if err != nil {
			return store.ProjectRow{}, err
		}
		if p.Retired {
			return store.ProjectRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "project %q is retired", name)
		}
		return p, nil
	}
	all, err := store.ListProjects(ctx, q)
	if err != nil {
		return store.ProjectRow{}, err
	}
	var active []store.ProjectRow
	var names []string
	for _, p := range all {
		if !p.Retired {
			active = append(active, p)
			names = append(names, p.Project)
		}
	}
	if len(active) == 1 {
		return active[0], nil
	}
	return store.ProjectRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "name the task's project, one of: %s", strings.Join(names, ", "))
}

// activeProjects returns the names of the active projects, sorted.
func activeProjects(ctx context.Context, q store.Querier) ([]string, error) {
	all, err := store.ListProjects(ctx, q)
	var names []string
	for _, p := range all {
		if !p.Retired {
			names = append(names, p.Project)
		}
	}
	return names, err
}
